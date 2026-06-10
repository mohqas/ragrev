// Package review executes the ML code review flow based on ragrev's vector db.
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/canonical/ragrev/internal/config"
	ghclient "github.com/canonical/ragrev/internal/github"
	"github.com/canonical/ragrev/internal/gitutil"
	"github.com/canonical/ragrev/internal/llm"
	"github.com/canonical/ragrev/internal/logger"
	"github.com/canonical/ragrev/internal/parser"
	"github.com/canonical/ragrev/internal/vectordb"
	gh "github.com/google/go-github/v72/github"
)

// ReviewMode determines whether to review a PR or local commits.
type ReviewMode string

const (
	ModePR    ReviewMode = "pr"
	ModeLocal ReviewMode = "local"
)

// Params holds parameters for the review command.
type Params struct {
	DBPath             string
	CollectionName     string
	Mode               ReviewMode
	PRNumber           int     // for PR mode
	Commits            []string // for local mode
	Config             config.Config
	UseAgentsOverrides bool
	Submit             bool
}

var snippetRe = regexp.MustCompile(`(?s)<snippet>\n?(.*?)\n?</snippet>`)

// Run executes the review for a specific PR or local commits.
func Run(ctx context.Context, params Params, log *logger.Logger) error {
	cfg := params.Config

	// Validate mode-specific requirements
	if params.Mode == ModePR && cfg.Repo == "" {
		return fmt.Errorf("repo is required for PR reviews")
	}

	if params.Mode == ModeLocal && params.Submit {
		return fmt.Errorf("--submit is only available when reviewing a PR")
	}

	backendName := cfg.Backend
	if backendName == "" {
		backendName = "github"
	}

	backendCfg, ok := cfg.Backends[backendName]
	if !ok {
		return fmt.Errorf("backend %q not found in config", backendName)
	}

	provider, err := llm.NewProvider(backendName, backendCfg, log)
	if err != nil {
		return fmt.Errorf("initializing LLM provider: %w", err)
	}

	// 2. Open DB
	store, err := vectordb.Open(params.DBPath, params.CollectionName)
	if err != nil {
		return fmt.Errorf("opening vector store: %w", err)
	}
	if store.Count() == 0 {
		return fmt.Errorf("vector store is empty, please run 'ragrev generate' first")
	}

	// 3. Fetch files based on mode
	var files []reviewFile
	var getFileContent func(string) (string, error)

	if params.Mode == ModePR {
		files, getFileContent, err = fetchPRFiles(ctx, params, cfg, log)
		if err != nil {
			return err
		}
	} else {
		files, getFileContent, err = fetchLocalFiles(params, log)
		if err != nil {
			return err
		}
	}

	var reviewParts []string
	var draftComments []*gh.DraftReviewComment

	// 1. Collect unique AGENTS.md files across all changed files
	var agentsMsg string
	if params.UseAgentsOverrides {
		if topLvl, err := gitutil.TopLevelDir(); err == nil {
			var agentFilesContext []string
			seenFiles := make(map[string]bool)

			for _, f := range files {
				if f.Status == "removed" {
					continue
				}

				dir := filepath.Dir(f.Filename)
				currentDir := topLvl

				var dirsToCheck []string
				dirsToCheck = append(dirsToCheck, topLvl)

				if dir != "." && dir != "" {
					parts := strings.Split(filepath.ToSlash(dir), "/")
					for _, part := range parts {
						if part == "" {
							continue
						}
						currentDir = filepath.Join(currentDir, part)
						dirsToCheck = append(dirsToCheck, currentDir)
					}
				}

				for _, d := range dirsToCheck {
					p1 := filepath.Join(d, "AGENTS.md")
					p2 := filepath.Join(d, "kb", "AGENTS.md")

					for _, p := range []string{p1, p2} {
						if seenFiles[p] {
							continue
						}
						seenFiles[p] = true
						if data, err := os.ReadFile(p); err == nil {
							rel, _ := filepath.Rel(topLvl, p)
							agentFilesContext = append(agentFilesContext, fmt.Sprintf("--- %s ---\n%s", rel, string(data)))
						}
					}
				}
			}

			if len(agentFilesContext) > 0 {
				agentsMsg = strings.Join(agentFilesContext, "\n\n")
			}
		}
	}

	// 2. Build the shared system message
	sysMsg := llm.Message{
		Role: "system",
		Content: fmt.Sprintf("You are a senior code reviewer acting as the repository code owner. The current date is %s. ", time.Now().Format("January 2, 2006")) +
			"You are reviewing an entire Pull Request that may span multiple files. I will provide you with the full content of a file, followed by chunks of unified diffs for that file where lines starting with '+' are additions and '-' are removals. " +
			"Context lines are shown for reference only \u2014 do not comment on them. " +
			"Focus exclusively on the added lines, but use your knowledge of previously reviewed files in this PR to spot cross-file inconsistencies. " +
			"Flag real issues: logic errors, missing error handling, naming inconsistencies, and style violations. " +
			"If a type or function appears missing in a chunk, ALWAYS check the FULL FILE CONTEXT (or context from previous files) before flagging it. " +
			"If an example past review is highly relevant, optionally mention it. " +
			"If your review applies ONLY to a specific section of the chunk, wrap those literal lines of code exactly as they appear in the diff inside <snippet> and </snippet> tags at the start of your response. Ensure the snippet exactly matches the diff text.\n" +
			"Be concise. If you find no issues in a chunk, respond with exactly LGTM_NO_ISSUES and nothing else.",
	}

	if agentsMsg != "" {
		sysMsg.Content += "\n\nCRITICAL CONTEXT: The repository contains an AGENTS.md file with specific technical instructions for AI tools. You MUST strictly adhere to these rules when reviewing:\n\n" + agentsMsg
	}

	conversation := []llm.Message{sysMsg}

	// 3. Process files iteratively within the shared conversation
	for _, f := range files {
		if f.Status == "removed" || len(strings.TrimSpace(f.Patch)) == 0 {
			continue
		}

		fullContent, _ := getFileContent(f.Filename)

		p := parser.ForFile(f.Filename)
		var chunks []parser.Chunk
		if p != nil {
			if fullContent != "" {
				chunks, _ = p.ParseFull(fullContent, f.Patch)
			} else {
				chunks, _ = p.Parse(f.Patch)
			}
		}
		if len(chunks) == 0 {
			chunks = parser.FallbackChunk(f.Patch)
		}

		var fileReviewParts []string

		// Announce file boundary and inject full file context as a user message
		fileContextMsg := fmt.Sprintf("--- Reviewing file: `%s` ---\n", f.Filename)
		if fullContent != "" {
			fileContextMsg += fmt.Sprintf("For reference, here is the full content of `%s` at the PR head:\n\n```\n%s\n```\n", f.Filename, fullContent)
		} else {
			fileContextMsg += "Full file context is not available for this file.\n"
		}

		conversation = append(conversation, llm.Message{
			Role:    "user",
			Content: fileContextMsg,
		})
		conversation = append(conversation, llm.Message{
			Role:    "assistant",
			Content: "Understood. Please provide the diff chunks for this file.",
		})

		for _, chunk := range chunks {
			if strings.TrimSpace(chunk.Patch) == "" {
				continue
			}

			// Retrieve relevant persona threads per chunk
			maxExamples := cfg.MaxExamples
			if maxExamples <= 0 {
				maxExamples = 2 // fallback if not set or invalid
			}

			results, err := store.QueryByAuthors(ctx, chunk.Patch, maxExamples, cfg.Reviewers)
			if err != nil {
				return fmt.Errorf("querying vector db for file %s chunk %s: %w", f.Filename, chunk.Name, err)
			}

			var pastReviewText string

			// Build examples from high confidence DB results.
			for _, r := range results {
				if r.Score > 0.6 {
					// Convert metadata string into an array of authors for this query
					var authorFilter map[string]bool
					if len(cfg.Reviewers) > 0 {
						authorFilter = make(map[string]bool, len(cfg.Reviewers))
						for _, rev := range cfg.Reviewers {
							authorFilter[rev] = true
						}
					}

					comments := vectordb.ExtractCommentsByAuthors(r.Metadata, authorFilter)
					for _, c := range comments {
						pastReviewText += fmt.Sprintf("Example past review comment on similar code (similarity %.2f", r.Score)
						if url, ok := r.Metadata["url"]; ok && url != "" {
							pastReviewText += fmt.Sprintf(", PR: %s", url)
						}
						pastReviewText += fmt.Sprintf("):\n%s\n\n", c.Body)
					}
				}
			}

			userContent := ""
			if pastReviewText != "" {
				userContent += "Related past reviews for context:\n" + pastReviewText + "\n"
			}

			if chunk.Kind == "overhead" {
				userContent += "\nCRITICAL CONTEXT: This diff chunk is named 'overhead' because it contains a collection of miscellaneous diff lines (like imports, global variables, or comments) that were not assigned to a specific function or block. Since these lines can be disjointed and skip over the actual declarations present in the full file, it is completely expected for types or functions to seem 'missing' in this chunk. DO NOT log an issue about a missing declaration or implementation in the overhead chunk. Additionally, completely ignore any stylistic spacing, blank line rules, or formatting issues in this chunk, as it is a synthetic concatenation of skipped lines and its vertical spacing will not reflect the actual file.\n\n"
			}

			userContent += fmt.Sprintf("Review the following diff chunk for `%s` (kind: %s, name: %s):\n\n```diff\n%s\n```", f.Filename, chunk.Kind, chunk.Name, chunk.Patch)

			userMsg := llm.Message{
				Role:    "user",
				Content: userContent,
			}
			conversation = append(conversation, userMsg)

			log.Print("Generating review for %s chunk: %s...\n", f.Filename, chunk.Name)
			chunkReview, err := provider.Complete(ctx, conversation)
			if err != nil {
				if strings.Contains(err.Error(), "tokens_limit_reached") {
					log.Verbose("  -> Skipping chunk %s of %s: prompt exceeded model token limit.\n\n", chunk.Name, f.Filename)
					fileReviewParts = append(fileReviewParts, fmt.Sprintf("#### `%s`\n_Skipped context window limit exceeded._", chunk.Name))

					// Revert the last user message to unblock the current chunk
					conversation = conversation[:len(conversation)-1]

					// To recover from a deep conversation buildup, remove the oldest 50% of the conversation history.
					// We always keep index 0 (sysMsg) and preserve the most recent half of the context.
					if len(conversation) > 3 {
						sysMsg := conversation[0]
						historyLen := len(conversation) - 1
						keepLen := historyLen / 2

						// Ensure we keep at least the last few turns to not completely lose immediate context
						if keepLen < 2 && historyLen >= 2 {
							keepLen = 2
						}

						tail := conversation[len(conversation)-keepLen:]
						newConv := []llm.Message{sysMsg}
						newConv = append(newConv, tail...)
						conversation = newConv
						log.Verbose("  -> Pruned oldest 50%% of conversation history due to token limit.\n")
					}

					continue
				}
				return fmt.Errorf("calling LLM provider for %s: %w", f.Filename, err)
			}

			conversation = append(conversation, llm.Message{Role: "assistant", Content: chunkReview})
			log.Verbose("--- Review for %s chunk %s ---\n%s\n\n", f.Filename, chunk.Name, chunkReview)

			cleanedReview := strings.TrimSpace(chunkReview)
			if !strings.Contains(cleanedReview, "LGTM_NO_ISSUES") {
				fileReviewParts = append(fileReviewParts, fmt.Sprintf("#### `%s`\n%s", chunk.Name, cleanedReview))

				if params.Submit {
					startLine := chunk.StartLine
					commentLine := chunk.EndLine

					// Check for sub-chunk snippet matching
					matches := snippetRe.FindStringSubmatch(cleanedReview)
					if len(matches) > 1 {
						snippetText := matches[1]
						idx := strings.Index(chunk.Patch, snippetText)
						if idx != -1 {
							linesBefore := strings.Count(chunk.Patch[:idx], "\n")
							snippetLines := strings.Count(snippetText, "\n") + 1
							startLineOff := linesBefore
							endLineOff := linesBefore + snippetLines - 1

							var sLine, eLine int
							for i := startLineOff; i < len(chunk.TargetLines) && i <= endLineOff; i++ {
								if chunk.TargetLines[i] > 0 {
									if sLine == 0 {
										sLine = chunk.TargetLines[i]
									}
									eLine = chunk.TargetLines[i]
								}
							}
							if sLine > 0 && eLine > 0 {
								startLine = sLine
								commentLine = eLine
							}
						}
						cleanedReview = snippetRe.ReplaceAllString(cleanedReview, "")
						cleanedReview = strings.TrimSpace(cleanedReview)
					}

					if commentLine == 0 {
						commentLine = 1
					}
					// Variables required for pointer fields
					filenamePath := f.Filename
					side := "RIGHT"
					commentBody := cleanedReview

					draftComment := &gh.DraftReviewComment{
						Path: &filenamePath,
						Line: &commentLine,
						Side: &side,
						Body: &commentBody,
					}

					if startLine > 0 && startLine < commentLine {
						draftComment.StartLine = &startLine
						draftComment.StartSide = &side
					}

					draftComments = append(draftComments, draftComment)
				}
			}
		}

		if len(fileReviewParts) > 0 {
			reviewParts = append(reviewParts, fmt.Sprintf("### `%s`\n%s", f.Filename, strings.Join(fileReviewParts, "\n\n")))
		}
	}

	if len(reviewParts) > 0 {
		log.Print("\n--- Full Review ---\n\n")
		log.Print("%s\n", strings.Join(reviewParts, "\n\n---\n\n"))

		if params.Submit && len(draftComments) > 0 {
			log.Print("Submitting pending review with %d comments...\n", len(draftComments))
			// We need the client for submission - only available in PR mode
			client, err := ghclient.NewClient(ctx)
			if err != nil {
				return fmt.Errorf("creating github client for submission: %w", err)
			}
			if err := client.SetRepo(cfg.Repo); err != nil {
				return fmt.Errorf("setting repo for submission: %w", err)
			}
			if err := client.CreatePendingReview(params.PRNumber, draftComments); err != nil {
				return fmt.Errorf("submitting review to GH: %w", err)
			}
			log.Print("Review submitted as PENDING.\n")
		}
	} else {
		log.Print("No issues found to report.\n")
	}

	return nil
}

// reviewFile is a common interface for files from both PR and local sources.
type reviewFile struct {
	Filename string
	Status   string
	Patch    string
}

// fetchPRFiles fetches files from a GitHub PR.
func fetchPRFiles(ctx context.Context, params Params, cfg config.Config, log *logger.Logger) ([]reviewFile, func(string) (string, error), error) {
	client, err := ghclient.NewClient(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("creating github client: %w", err)
	}
	if err := client.SetRepo(cfg.Repo); err != nil {
		return nil, nil, fmt.Errorf("setting repo: %w", err)
	}

	log.Print("Fetching files for PR #%d from %s...\n", params.PRNumber, client.RepoFullName())
	ghFiles, err := client.ListPRFiles(params.PRNumber)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching PR files: %w", err)
	}

	var files []reviewFile
	for _, f := range ghFiles {
		files = append(files, reviewFile{
			Filename: f.Filename,
			Status:   f.Status,
			Patch:    f.Patch,
		})
	}

	getContent := func(filename string) (string, error) {
		content, err := client.GetPRHeadFileContent(params.PRNumber, filename)
		if err != nil {
			return "", err
		}
		return content, nil
	}

	return files, getContent, nil
}

// fetchLocalFiles fetches files from local git commits.
func fetchLocalFiles(params Params, log *logger.Logger) ([]reviewFile, func(string) (string, error), error) {
	commits := params.Commits

	// Auto-detect unpushed commits if not specified
	if len(commits) == 0 {
		log.Verbose("No commits specified, detecting unpushed commits...\n")
		var err error
		commits, err = gitutil.UnpushedCommits()
		if err != nil {
			return nil, nil, fmt.Errorf("detecting unpushed commits: %w", err)
		}
		if len(commits) == 0 {
			return nil, nil, fmt.Errorf("no unpushed commits found; specify commits explicitly or push first")
		}
		log.Print("Found %d unpushed commit(s)\n", len(commits))
	}

	log.Print("Reviewing %d commit(s): %s\n", len(commits), strings.Join(commits, ", "))

	gitFiles, err := gitutil.ChangedFiles(commits)
	if err != nil {
		return nil, nil, fmt.Errorf("getting changed files: %w", err)
	}

	var files []reviewFile
	for _, f := range gitFiles {
		files = append(files, reviewFile{
			Filename: f.Filename,
			Status:   f.Status,
			Patch:    f.Patch,
		})
	}

	// Use the last commit to fetch file content
	lastCommit := commits[len(commits)-1]
	getContent := func(filename string) (string, error) {
		content, err := gitutil.FileContentAtCommit(lastCommit, filename)
		if err != nil {
			return "", err
		}
		return content, nil
	}

	return files, getContent, nil
}

