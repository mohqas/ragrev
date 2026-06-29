// Package review executes the ML code review flow based on ragrev's vector db.
package review

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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
	PRNumber           int      // for PR mode
	Commits            []string // for local mode
	Config             config.Config
	UseAgentsOverrides bool
	Submit             bool
	// Scope overrides Config.ReviewScope when non-empty.
	Scope ScopeMode
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
		// Distinguish "never initialized" from "initialized but no past comments".
		if !vectordb.Initialized(params.DBPath) {
			return fmt.Errorf("no review index found; run 'ragrev init' first")
		}
		log.Print("Review index is empty (no past review comments found); " +
			"proceeding with a generic review without historical context.\n")
	}

	// Resolve scope (param overrides config; config falls back to "pr").
	scopeStr := string(params.Scope)
	if scopeStr == "" {
		scopeStr = cfg.ReviewScope
	}
	scope, err := validateScope(scopeStr)
	if err != nil {
		return err
	}
	log.Verbose("Review scope: %s\n", scope)

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

	// Sort files for deterministic ordering (helps with prompt caching too).
	sort.SliceStable(files, func(i, j int) bool {
		return files[i].Filename < files[j].Filename
	})

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
			"You are reviewing an entire Pull Request that may span multiple files. " +
			"Each file's full content is introduced with a header in the form `===== FILE: <path> =====` followed by the file body. " +
			"Diff chunks for review are prefixed with `FILE: <path>` and `CHUNK: <name>` so you always know which file they belong to, regardless of the order chunks arrive. " +
			"In a unified diff, lines starting with '+' are additions and '-' are removals; context lines are shown for reference only — do not comment on them. " +
			"Focus exclusively on the added lines, but freely cross-reference any file in this PR to spot inconsistencies. " +
			"Flag real issues: logic errors, missing error handling, naming inconsistencies, and style violations. " +
			"If a type or function appears missing in a chunk, ALWAYS check the FULL FILE CONTEXT (or context from other files in this PR) before flagging it. " +
			"If an example past review is highly relevant, optionally mention it. " +
			"If your review applies ONLY to a specific section of the chunk, wrap those literal lines of code exactly as they appear in the diff inside <snippet> and </snippet> tags at the start of your response. Ensure the snippet exactly matches the diff text.\n" +
			"Be concise. If you find no issues in a chunk, respond with exactly LGTM_NO_ISSUES and nothing else.",
	}

	if agentsMsg != "" {
		sysMsg.Content += "\n\nCRITICAL CONTEXT: The repository contains an AGENTS.md file with specific technical instructions for AI tools. You MUST strictly adhere to these rules when reviewing:\n\n" + agentsMsg
	}

	budget := provider.MaxPromptTokens()
	// Reserve room for the model's response and per-chunk overhead.
	const responseReserve = 4096
	const perChunkReserve = 2048
	effectiveBudget := budget - responseReserve - perChunkReserve
	if effectiveBudget < 1024 {
		effectiveBudget = 1024
	}
	log.Verbose("LLM budget: %d prompt tokens (effective %d after reserves)\n", budget, effectiveBudget)

	conv := newConversation(sysMsg, budget, log)

	// Aggregate token usage across all LLM calls for the post-review summary.
	stats := usageStats{model: provider.Model()}

	// Per-file cached full content (avoids double-fetching for preload + lazy injection).
	fileContents := make(map[string]string, len(files))
	loadContent := func(name string) string {
		if v, ok := fileContents[name]; ok {
			return v
		}
		content, _ := getFileContent(name)
		fileContents[name] = content
		return content
	}

	// preloaded tracks files whose full content currently lives in conv as a
	// preload entry (used to decide whether to inline file context inside a
	// chunk message).
	preloaded := make(map[string]bool)
	// completedFiles tracks files whose chunks have all been reviewed; the
	// pruner prefers evicting these first.
	completedFiles := make(map[string]bool)

	// Helper: build the per-file context header user-message.
	fileContextMessage := func(filename, content string) llm.Message {
		body := fmt.Sprintf("===== FILE: %s =====\n", filename)
		if content != "" {
			body += fmt.Sprintf("Full content of `%s` at the PR head:\n\n```\n%s\n```\n", filename, content)
		} else {
			body += fmt.Sprintf("Full content of `%s` is not available.\n", filename)
		}
		return llm.Message{Role: "user", Content: body}
	}

	// ---- Scope=PR: preload all file contents up front ----
	if scope == ScopePR {
		// Determine which files have at least one reviewable chunk so we don't
		// waste budget on no-op files.
		preloadCandidates := make([]reviewFile, 0, len(files))
		for _, f := range files {
			if f.Status == "removed" || len(strings.TrimSpace(f.Patch)) == 0 {
				continue
			}
			preloadCandidates = append(preloadCandidates, f)
		}

		// Build per-file token estimates.
		type sized struct {
			file    reviewFile
			content string
			tokens  int
		}
		sizes := make([]sized, 0, len(preloadCandidates))
		baseTokens := llm.EstimateTokens(sysMsg.Content) + 4
		totalTokens := baseTokens
		for _, f := range preloadCandidates {
			content := loadContent(f.Filename)
			msg := fileContextMessage(f.Filename, content)
			t := llm.EstimateTokens(msg.Content) + 4
			sizes = append(sizes, sized{file: f, content: content, tokens: t})
			totalTokens += t
		}

		// Drop the largest files first until everything fits.
		// Keep at least 1 preload if it would still fit, otherwise we'll fall
		// back to per-file lazy injection for everything.
		dropped := make(map[string]bool)
		if totalTokens > effectiveBudget {
			log.Verbose("Preload exceeds budget (%d > %d); dropping largest files.\n", totalTokens, effectiveBudget)
			ordered := make([]int, len(sizes))
			for i := range sizes {
				ordered[i] = i
			}
			sort.Slice(ordered, func(a, b int) bool {
				return sizes[ordered[a]].tokens > sizes[ordered[b]].tokens
			})
			for _, idx := range ordered {
				if totalTokens <= effectiveBudget {
					break
				}
				dropped[sizes[idx].file.Filename] = true
				totalTokens -= sizes[idx].tokens
				log.Verbose("  -> Skipping preload for %s (%d tokens).\n", sizes[idx].file.Filename, sizes[idx].tokens)
			}
		}

		preloadedAny := false
		for _, s := range sizes {
			if dropped[s.file.Filename] {
				continue
			}
			conv.append(convEntry{
				msg:  fileContextMessage(s.file.Filename, s.content),
				kind: kindPreloadFile,
				file: s.file.Filename,
			})
			preloaded[s.file.Filename] = true
			preloadedAny = true
		}
		if preloadedAny {
			conv.append(convEntry{
				msg:  llm.Message{Role: "assistant", Content: "Understood. I have the file contents for this PR. Please send the diff chunks."},
				kind: kindPreloadAck,
			})
		}
	}

	// Track lazy-injection state for scope=file.
	announcedFile := ""

	// 3. Process files iteratively
	for _, f := range files {
		if f.Status == "removed" || len(strings.TrimSpace(f.Patch)) == 0 {
			completedFiles[f.Filename] = true
			continue
		}

		fullContent := loadContent(f.Filename)

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

		// Scope=file: announce file boundary lazily, once per file, with full content.
		if scope == ScopeFile && announcedFile != f.Filename {
			conv.append(convEntry{
				msg:  fileContextMessage(f.Filename, fullContent),
				kind: kindFileHeader,
				file: f.Filename,
			})
			conv.append(convEntry{
				msg:  llm.Message{Role: "assistant", Content: "Understood. Please provide the diff chunks for this file."},
				kind: kindFileHeaderAck,
				file: f.Filename,
			})
			preloaded[f.Filename] = true
			announcedFile = f.Filename
		}

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

			log.Debug("DB results for %s / %s: %d candidate(s)\n", f.Filename, chunk.Name, len(results))

			// Build examples from high confidence DB results.
			for i, r := range results {
				log.Debug("  [%d] score=%.4f url=%s\n", i, r.Score, r.Metadata["url"])
				if float64(r.Score) >= cfg.MinSimilarityScore {
					// Convert metadata string into an array of authors for this query
					var authorFilter map[string]bool
					if len(cfg.Reviewers) > 0 {
						authorFilter = make(map[string]bool, len(cfg.Reviewers))
						for _, rev := range cfg.Reviewers {
							authorFilter[rev] = true
						}
					}

					log.Debug("       code: %s\n", truncate(r.Content, 300))
					comments := vectordb.ExtractCommentsByAuthors(r.Metadata, authorFilter)
					log.Debug("       -> above threshold: %d comment(s) after author filter\n", len(comments))
					for _, c := range comments {
						log.Debug("          author=%s body=%q\n", c.Author, truncate(c.Body, 120))
						pastReviewText += fmt.Sprintf("Example past review comment on similar code (similarity %.2f", r.Score)
						if url, ok := r.Metadata["url"]; ok && url != "" {
							pastReviewText += fmt.Sprintf(", PR: %s", url)
						}
						pastReviewText += fmt.Sprintf("):\n%s\n\n", c.Body)
					}
				}
			}

			userContent := fmt.Sprintf("FILE: %s\nCHUNK: %s (kind: %s)\n\n", f.Filename, chunk.Name, chunk.Kind)

			// Scope=chunk OR PR-scope file that was dropped from preload:
			// inline the file content with the chunk.
			needInlineContent := false
			switch scope {
			case ScopeChunk:
				needInlineContent = true
			case ScopePR:
				if !preloaded[f.Filename] && !conv.hasFile(f.Filename) {
					needInlineContent = true
				}
			}
			if needInlineContent && fullContent != "" {
				userContent += fmt.Sprintf("Full content of `%s` at the PR head (for reference):\n\n```\n%s\n```\n\n", f.Filename, fullContent)
			} else if needInlineContent {
				userContent += "Full file content is not available for this file.\n\n"
			}

			if pastReviewText != "" {
				userContent += "Related past reviews for context:\n" + pastReviewText + "\n"
			}

			if chunk.Kind == "overhead" {
				userContent += "\nCRITICAL CONTEXT: This diff chunk is named 'overhead' because it contains a collection of miscellaneous diff lines (like imports, global variables, or comments) that were not assigned to a specific function or block. Since these lines can be disjointed and skip over the actual declarations present in the full file, it is completely expected for types or functions to seem 'missing' in this chunk. DO NOT log an issue about a missing declaration or implementation in the overhead chunk. Additionally, completely ignore any stylistic spacing, blank line rules, or formatting issues in this chunk, as it is a synthetic concatenation of skipped lines and its vertical spacing will not reflect the actual file.\n\n"
			}

			userContent += fmt.Sprintf("Review the following diff chunk:\n\n```diff\n%s\n```", chunk.Patch)

			// For scope=chunk we run an isolated call so the conversation never
			// grows beyond [sysMsg, userMsg]. We snapshot/restore conv around
			// the call to keep the same code path for token-limit recovery.
			var convForCall *conversation
			if scope == ScopeChunk {
				convForCall = newConversation(sysMsg, budget, log)
				convForCall.append(convEntry{
					msg:  llm.Message{Role: "user", Content: userContent},
					kind: kindChunkUser,
					file: f.Filename,
				})
			} else {
				conv.append(convEntry{
					msg:  llm.Message{Role: "user", Content: userContent},
					kind: kindChunkUser,
					file: f.Filename,
				})
				convForCall = conv
			}

			// Proactive pre-call sizing: if we're over budget already, prune
			// before bothering the API.
			if convForCall.estTokens() > effectiveBudget {
				convForCall.prune(completedFiles, effectiveBudget)
			}

			log.Print("Generating review for %s chunk: %s...\n", f.Filename, chunk.Name)
			chunkReview, usage, err := provider.Complete(ctx, convForCall.messages())
			if err != nil {
				if strings.Contains(err.Error(), "tokens_limit_reached") {
					log.Verbose("  -> tokens_limit_reached for chunk %s of %s; attempting recovery.\n", chunk.Name, f.Filename)

					if scope == ScopeChunk {
						// Nothing to prune in an isolated conversation.
						stats.skipped++
						fileReviewParts = append(fileReviewParts, fmt.Sprintf("#### `%s`\n_Skipped: context window limit exceeded._", chunk.Name))
						continue
					}

					// Roll back the just-appended chunkUser message.
					conv.dropLastN(1)
					// Aggressively prune.
					target := effectiveBudget / 2
					if target < 1024 {
						target = 1024
					}
					conv.prune(completedFiles, target)

					// Retry once with the same chunk content.
					conv.append(convEntry{
						msg:  llm.Message{Role: "user", Content: userContent},
						kind: kindChunkUser,
						file: f.Filename,
					})
					chunkReview, usage, err = provider.Complete(ctx, conv.messages())
					if err != nil {
						if strings.Contains(err.Error(), "tokens_limit_reached") {
							log.Verbose("  -> Still over limit after prune; skipping chunk %s of %s.\n\n", chunk.Name, f.Filename)
							stats.skipped++
							fileReviewParts = append(fileReviewParts, fmt.Sprintf("#### `%s`\n_Skipped: context window limit exceeded._", chunk.Name))
							conv.dropLastN(1)
							continue
						}
						return fmt.Errorf("calling LLM provider for %s after prune: %w", f.Filename, err)
					}
					stats.retries++
				} else {
					return fmt.Errorf("calling LLM provider for %s: %w", f.Filename, err)
				}
			}

			// Record usage for the summary.
			stats.add(usage)

			// Record assistant reply only for non-chunk scopes (chunk scope
			// intentionally discards history between calls).
			if scope != ScopeChunk {
				conv.append(convEntry{
					msg:  llm.Message{Role: "assistant", Content: chunkReview},
					kind: kindChunkAssistant,
					file: f.Filename,
				})
			}
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

		// Mark this file as fully reviewed so its preloaded content becomes
		// a prune candidate before content for files we haven't reached yet.
		completedFiles[f.Filename] = true

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

	// Token usage summary.
	stats.report(log)

	return nil
}

// reviewFile is a common interface for files from both PR and local sources.
type reviewFile struct {
	Filename string
	Status   string
	Patch    string
}

// truncate shortens s to at most n runes, appending "…" when cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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
