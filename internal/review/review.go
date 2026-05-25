// Package review executes the ML code review flow based on ragrev's vector db.
package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mohqas/ragrev/internal/config"
	ghclient "github.com/mohqas/ragrev/internal/github"
	"github.com/mohqas/ragrev/internal/gitutil"
	"github.com/mohqas/ragrev/internal/vectordb"
)

const (
	githubModelsURL = "https://models.inference.ai.azure.com/chat/completions"
	reviewModel     = "o4-mini"
)

// Params holds parameters for the review command.
type Params struct {
	DBPath             string
	CollectionName     string
	PRNumber           int
	Config             config.Config
	UseAgentsOverrides bool
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

// Run executes the review for a specific PR.
func Run(ctx context.Context, params Params, w io.Writer) error {
	cfg := params.Config

	if cfg.Repo == "" {
		return fmt.Errorf("repo is required")
	}

	// 1. Get Github Token
	token, err := ghclient.Token()
	if err != nil {
		return fmt.Errorf("fetching github token (needed for API and Review Models): %w", err)
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

		var pastReviewMessages []chatMessage

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

					pastReviewMessages = append(pastReviewMessages, chatMessage{
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
				agentsPath := filepath.Join(topLvl, "AGENTS.md")
				if data, err := os.ReadFile(agentsPath); err == nil {
					agentsMsg = string(data)
				}
			}
		}

		sysMsg := chatMessage{
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

		userMsg := chatMessage{
			Role:    "user",
			Content: fmt.Sprintf("Review the following diff for `%s`:\n\n```diff\n%s\n```", f.Filename, f.Patch),
		}

		messages := []chatMessage{sysMsg}
		messages = append(messages, pastReviewMessages...)
		messages = append(messages, userMsg)

		reqBody := chatRequest{
			Model:    reviewModel,
			Messages: messages,
		}

		fmt.Fprintf(w, "Generating review for %s...\n", f.Filename)
		reviewText, err := callGitHubModels(ctx, reqBody, token, w)
		if err != nil {
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

func callGitHubModels(ctx context.Context, reqBody chatRequest, token string, w io.Writer) (string, error) {
	b, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	maxRetries := 5
	backoff := 22 * time.Second // starting with 22s since the limit is 3 per 60s

	for attempt := 0; attempt <= maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", githubModelsURL, bytes.NewReader(b))
		if err != nil {
			return "", err
		}

		req.Header.Set("Content-Type", "application/json")
		// GitHub Models API accepts standard GitHub tokens. Note it expects a Bearer token or api-key.
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return "", err
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			resp.Body.Close()
			if attempt == maxRetries {
				return "", fmt.Errorf("github models API error: rate limit exceeded after %d retries", maxRetries)
			}
			delay := backoff
			if headerVal := resp.Header.Get("Retry-After"); headerVal != "" {
				var waitSecs int
				if _, err := fmt.Sscanf(headerVal, "%d", &waitSecs); err == nil && waitSecs > 0 {
					delay = time.Duration(waitSecs) * time.Second
				}
			}
			delay += time.Duration(rand.Float64() * float64(time.Second))
			fmt.Fprintf(w, "  Rate limit reached (429). Retrying in %v...\n", delay.Round(time.Second))
			select {
			case <-time.After(delay):
				backoff *= 2
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			// Azure / GitHub models sometimes return RateLimitReached as a 400 or 403 or embed it in JSON
			if strings.Contains(string(body), "RateLimitReached") && attempt < maxRetries {
				delay := backoff
				// Try to extract the number of seconds if the message explicitly says it
				var waitSecs int
				if _, err := fmt.Sscanf(string(body), "Please wait %d seconds", &waitSecs); err == nil && waitSecs > 0 {
					delay = time.Duration(waitSecs) * time.Second
				}
				delay += time.Duration(rand.Float64() * float64(time.Second))
				fmt.Fprintf(w, "  Rate limit reached (RateLimitReached). Retrying in %v...\n", delay.Round(time.Second))
				select {
				case <-time.After(delay):
					backoff *= 2
					continue
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}

			return "", fmt.Errorf("github models API error (status %d): %s", resp.StatusCode, string(body))
		}

		var res chatResponse
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			resp.Body.Close()
			return "", err
		}
		resp.Body.Close()

		if len(res.Choices) == 0 {
			return "", fmt.Errorf("no response choices returned")
		}
		return res.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("max retries exceeded without a valid response from Github Models")
}

