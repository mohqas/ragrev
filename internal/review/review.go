// Package review executes the ML code review flow based on ragrev's vector db.
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gh "github.com/google/go-github/v72/github"
	"github.com/mohqas/ragrev/internal/config"
	ghclient "github.com/mohqas/ragrev/internal/github"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/mohqas/ragrev/internal/llm"
	"github.com/mohqas/ragrev/internal/logger"
	"github.com/mohqas/ragrev/internal/parser"
	"github.com/mohqas/ragrev/internal/vectordb"
)

// Params holds parameters for the review command.
type Params struct {
	DBPath             string
	CollectionName     string
	PRNumber           int
	Config             config.Config
	UseAgentsOverrides bool
	Submit             bool
}

// Run executes the review for a specific PR.
func Run(ctx context.Context, params Params, log *logger.Logger) error {
	cfg := params.Config

	if cfg.Repo == "" {
		return fmt.Errorf("repo is required")
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

	// 3. Connect to GitHub and fetch PR Files
	client, err := ghclient.NewClient(ctx)
	if err != nil {
		return fmt.Errorf("creating github client: %w", err)
	}
	if err := client.SetRepo(cfg.Repo); err != nil {
		return fmt.Errorf("setting repo: %w", err)
	}

	log.Print("Fetching files for PR #%d from %s...\n", params.PRNumber, client.RepoFullName())
	files, err := client.ListPRFiles(params.PRNumber)
	if err != nil {
		return fmt.Errorf("fetching PR files: %w", err)
	}

	var reviewParts []string
	var draftComments []*gh.DraftReviewComment

	for _, f := range files {
		if f.Status == "removed" || len(strings.TrimSpace(f.Patch)) == 0 {
			continue
		}

		// Attempt to load AGENTS.md if flag is active
		var agentsMsg string
		if params.UseAgentsOverrides {
			if topLvl, err := gitutil.TopLevelDir(); err == nil {
				var dirsToCheck []string
				dirsToCheck = append(dirsToCheck, topLvl)

				dir := filepath.Dir(f.Filename)
				currentDir := topLvl
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

				var agentFilesContext []string
				seenFiles := make(map[string]bool)

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

				if len(agentFilesContext) > 0 {
					agentsMsg = strings.Join(agentFilesContext, "\n\n")
				}
			}
		}

		p := parser.ForFile(f.Filename)
		var chunks []parser.Chunk
		if p != nil {
			chunks, _ = p.Parse(f.Patch)
		}
		if len(chunks) == 0 {
			chunks = parser.FallbackChunk(f.Patch)
		}

		var fileReviewParts []string

		sysMsg := llm.Message{
			Role: "system",
			Content: "You are a senior code reviewer acting as the repository code owner. " +
				"You will be given chunks of a unified diff where lines starting with '+' are additions " +
				"and lines starting with '-' are removals. Context lines (no prefix) are shown " +
				"for reference only \u2014 do not comment on them. " +
				"Focus exclusively on the added lines. " +
				"Flag real issues: logic errors, missing error handling, naming inconsistencies, " +
				"style violations, and code that contradicts patterns in the example past reviews below. " +
				"If a type, function, or variable appears to be missing or undeclared in the diff, ALWAYS check the FULL FILE CONTEXT provided below before flagging it as an error. " +
				"When making a suggestion, if an example past review has high similarity and is deeply relevant, " +
				"you may optionally mention it or link its PR as a reference for your suggestion. " +
				"Be concise and specific. If you find no issues in a chunk, respond with exactly LGTM_NO_ISSUES and nothing else.",
		}

		if agentsMsg != "" {
			sysMsg.Content += "\n\nCRITICAL CONTEXT: The repository contains an AGENTS.md file with specific technical instructions for AI tools. You MUST strictly adhere to these rules when reviewing:\n\n" + agentsMsg
		}

		fullContent, err := client.GetPRHeadFileContent(params.PRNumber, f.Filename)
		if err == nil && fullContent != "" {
			sysMsg.Content += fmt.Sprintf("\n\nFULL FILE CONTEXT:\nFor reference, here is the full content of `%s` at the PR head:\n\n```\n%s\n```\n", f.Filename, fullContent)
		}

		conversation := []llm.Message{sysMsg}

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
				userContent += "\nCRITICAL CONTEXT: This diff chunk is named 'overhead' because it contains a collection of miscellaneous diff lines (like imports, global variables, or comments) that were not assigned to a specific function or block. Since these lines can be disjointed and skip over the actual declarations present in the full file, it is completely expected for types or functions to seem 'missing' in this chunk. DO NOT log an issue about a missing declaration or implementation in the overhead chunk.\n\n"
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

					// Revert the last user message so the token usage does not permanently block subsequent chunks
					conversation = conversation[:len(conversation)-1]
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
					commentLine := chunk.EndLine
					if commentLine == 0 {
						commentLine = 1
					}
					// Variables required for pointer fields
					filenamePath := f.Filename
					side := "RIGHT"
					commentBody := cleanedReview

					draftComments = append(draftComments, &gh.DraftReviewComment{
						Path: &filenamePath,
						Line: &commentLine,
						Side: &side,
						Body: &commentBody,
					})
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
