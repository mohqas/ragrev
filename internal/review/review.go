// Package review executes the ML code review flow based on ragrev's vector db.
package review

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mohqas/ragrev/internal/config"
	ghclient "github.com/mohqas/ragrev/internal/github"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/mohqas/ragrev/internal/llm"
	"github.com/mohqas/ragrev/internal/vectordb"
)

// Params holds parameters for the review command.
type Params struct {
	DBPath             string
	CollectionName     string
	PRNumber           int
	Config             config.Config
	UseAgentsOverrides bool
}

// Run executes the review for a specific PR.
func Run(ctx context.Context, params Params, w io.Writer) error {
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

	provider, err := llm.NewProvider(backendName, backendCfg, w)
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

	fmt.Fprintf(w, "Fetching files for PR #%d from %s...\n", params.PRNumber, client.RepoFullName())
	files, err := client.ListPRFiles(params.PRNumber)
	if err != nil {
		return fmt.Errorf("fetching PR files: %w", err)
	}

	var reviewParts []string

	for _, f := range files {
		if f.Status == "removed" || len(strings.TrimSpace(f.Patch)) == 0 {
			continue
		}

		// Retrieve relevant persona threads
		results, err := store.QueryByAuthors(ctx, f.Patch, 4, cfg.Reviewers)
		if err != nil {
			return fmt.Errorf("querying vector db for file %s: %w", f.Filename, err)
		}

		var pastReviewMessages []llm.Message

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
					msg := fmt.Sprintf("Example past review comment on similar code (similarity %.2f", r.Score)
					if url, ok := r.Metadata["url"]; ok && url != "" {
						msg += fmt.Sprintf(", PR: %s", url)
					}
					msg += fmt.Sprintf("): %s", c.Body)

					pastReviewMessages = append(pastReviewMessages, llm.Message{
						Role:    "assistant",
						Content: msg,
					})
				}
			}
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

		sysMsg := llm.Message{
			Role: "system",
			Content: "You are a senior code reviewer acting as the repository code owner. " +
				"You will be given a unified diff where lines starting with '+' are additions " +
				"and lines starting with '-' are removals. Context lines (no prefix) are shown " +
				"for reference only \u2014 do not comment on them. " +
				"Focus exclusively on the added lines. " +
				"Flag real issues: logic errors, missing error handling, naming inconsistencies, " +
				"style violations, and code that contradicts patterns in the example past reviews below. " +
				"When making a suggestion, if an example past review has high similarity and is deeply relevant, " +
				"you may optionally mention it or link its PR as a reference for your suggestion. " +
				"Be concise and specific. If there are no issues, say so briefly.",
		}

		if agentsMsg != "" {
			sysMsg.Content += "\n\nCRITICAL CONTEXT: The repository contains an AGENTS.md file with specific technical instructions for AI tools. You MUST strictly adhere to these rules when reviewing:\n\n" + agentsMsg
		}

		userMsg := llm.Message{
			Role:    "user",
			Content: fmt.Sprintf("Review the following diff for `%s`:\n\n```diff\n%s\n```", f.Filename, f.Patch),
		}

		messages := []llm.Message{sysMsg}
		messages = append(messages, pastReviewMessages...)
		messages = append(messages, userMsg)

		fmt.Fprintf(w, "Generating review for %s...\n", f.Filename)
		reviewText, err := provider.Complete(ctx, messages)
		if err != nil {
			if strings.Contains(err.Error(), "tokens_limit_reached") {
				fmt.Fprintf(w, "  -> Skipping large file %s: prompt exceeded model token limit.\n\n", f.Filename)
				reviewParts = append(reviewParts, fmt.Sprintf("### `%s`\n_Skipped context window limit exceeded. File too large for model._", f.Filename))
				continue
			}
			return fmt.Errorf("calling GitHub models for %s: %w", f.Filename, err)
		}

		reviewParts = append(reviewParts, fmt.Sprintf("### `%s`\n%s", f.Filename, reviewText))
		fmt.Fprintf(w, "--- Review for %s ---\n%s\n\n", f.Filename, reviewText)
	}

	if len(reviewParts) > 0 {
		fmt.Fprintf(w, "\n--- Full Review ---\n\n")
		fmt.Fprintf(w, "%s\n", strings.Join(reviewParts, "\n\n---\n\n"))
	} else {
		fmt.Fprintf(w, "No reviewable files found.\n")
	}

	return nil
}
