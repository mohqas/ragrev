// Package github provides GitHub API access for fetching PR review comments.
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	gh "github.com/google/go-github/v72/github"
	"github.com/mohqas/ragrev/internal/logger"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v3"
)

// ThreadComment represents a single review comment within a conversation thread.
type ThreadComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// Thread represents a conversation thread on a single code diff hunk.
// Multiple review comments on the same code location are grouped together.
type Thread struct {
	// ID is the root comment ID (identifies the whole thread).
	ID string
	// DiffHunk is the code context that the thread is about.
	DiffHunk string
	// Path is the file path the comments are on.
	Path string
	// PRURL is the URL of the pull request.
	PRURL string
	// Comments is the ordered list of individual comments in this thread.
	Comments []ThreadComment
}

// Authors returns the unique set of authors in this thread.
func (t *Thread) Authors() []string {
	seen := make(map[string]bool)
	var result []string
	for _, c := range t.Comments {
		if !seen[c.Author] {
			seen[c.Author] = true
			result = append(result, c.Author)
		}
	}
	return result
}

// CommentsJSON returns the comments serialized as a JSON string
// for storage in vector DB metadata.
func (t *Thread) CommentsJSON() string {
	data, _ := json.Marshal(t.Comments)
	return string(data)
}

// ParseCommentsJSON deserializes a JSON string back into ThreadComments.
func ParseCommentsJSON(s string) ([]ThreadComment, error) {
	var comments []ThreadComment
	if err := json.Unmarshal([]byte(s), &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// FilterCommentsByAuthors returns only comments from the given authors.
// If authors is nil or empty, all comments are returned.
func FilterCommentsByAuthors(comments []ThreadComment, authors map[string]bool) []ThreadComment {
	if len(authors) == 0 {
		return comments
	}
	var result []ThreadComment
	for _, c := range comments {
		if authors[c.Author] {
			result = append(result, c)
		}
	}
	return result
}

// Client wraps the GitHub API.
type Client struct {
	api  *gh.Client
	ctx  context.Context
	repo *gh.Repository
	log  *logger.Logger
}

// NewClient creates a Client authenticated via GITHUB_TOKEN env var
// or falling back to the gh CLI config file.
func NewClient(ctx context.Context) (*Client, error) {
	token, err := resolveToken()
	if err != nil {
		return nil, fmt.Errorf("resolving github token: %w", err)
	}

	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(ctx, ts)
	api := gh.NewClient(httpClient)

	return &Client{api: api, ctx: ctx}, nil
}

// SetLogger attaches a logger so the client can emit debug output for
// retries and token refreshes. Safe to call with nil.
func (c *Client) SetLogger(log *logger.Logger) {
	c.log = log
}

// logDebug writes a debug message if a logger is attached.
func (c *Client) logDebug(format string, args ...any) {
	if c.log != nil {
		c.log.Debug(format, args...)
	}
}

// refreshToken re-resolves the GitHub token (env → gh CLI → hosts.yml) and
// rebuilds the underlying API client. Used to recover from 401 errors when a
// short-lived token expires mid-run.
func (c *Client) refreshToken() error {
	token, err := Token()
	if err != nil {
		return err
	}
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	httpClient := oauth2.NewClient(c.ctx, ts)
	c.api = gh.NewClient(httpClient)
	return nil
}

// withRetry runs fn with exponential backoff. fn must return the *gh.Response
// (used to detect HTTP status) and an error. On a 401 the token is refreshed
// before retrying so an expired credential doesn't abort a long run. GitHub
// rate-limit errors (primary 5000/h and secondary/abuse limits) are handled by
// waiting until the limit resets and retrying without consuming the normal
// attempt budget.
func (c *Client) withRetry(desc string, fn func() (*gh.Response, error)) error {
	const maxAttempts = 5
	const maxRateLimitWaits = 20
	backoff := 200 * time.Millisecond
	rateLimitWaits := 0

	var lastErr error
	for attempt := 1; ; {
		resp, err := fn()
		if err == nil {
			return nil
		}
		lastErr = err

		// Handle GitHub rate limiting explicitly: wait until it resets, then
		// retry. These waits don't count against maxAttempts.
		var rlErr *gh.RateLimitError
		var abuseErr *gh.AbuseRateLimitError
		if errors.As(err, &rlErr) {
			if rateLimitWaits >= maxRateLimitWaits {
				break
			}
			rateLimitWaits++
			wait := time.Until(rlErr.Rate.Reset.Time) + 2*time.Second
			if wait < 2*time.Second {
				wait = 2 * time.Second
			}
			c.logDebug("  github: rate limit exceeded (%s); waiting %v until reset\n",
				desc, wait.Round(time.Second))
			if !c.sleep(wait) {
				return c.ctx.Err()
			}
			continue
		}
		if errors.As(err, &abuseErr) {
			if rateLimitWaits >= maxRateLimitWaits {
				break
			}
			rateLimitWaits++
			wait := 60 * time.Second
			if abuseErr.RetryAfter != nil {
				wait = *abuseErr.RetryAfter
			}
			c.logDebug("  github: secondary rate limit (%s); waiting %v\n",
				desc, wait.Round(time.Second))
			if !c.sleep(wait) {
				return c.ctx.Err()
			}
			continue
		}

		status := 0
		if resp != nil {
			status = resp.StatusCode
		}

		// Refresh credentials on auth failures before the next attempt.
		if status == 401 {
			if rerr := c.refreshToken(); rerr != nil {
				c.logDebug("  github: token refresh after 401 failed: %v\n", rerr)
			} else {
				c.logDebug("  github: refreshed token after 401 (%s)\n", desc)
			}
		}

		if attempt >= maxAttempts {
			break
		}

		c.logDebug("  github: %s failed (attempt %d/%d): %v; retrying in %v\n",
			desc, attempt, maxAttempts, err, backoff)
		if !c.sleep(backoff) {
			return c.ctx.Err()
		}
		backoff *= 2
		attempt++
	}
	return lastErr
}

// sleep waits for d or until the client context is cancelled. It returns false
// if the context was cancelled (so callers can abort cleanly).
func (c *Client) sleep(d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// SetRepo configures the client to work with the given "owner/repo" string.
func (c *Client) SetRepo(nameWithOwner string) error {
	parts := strings.SplitN(nameWithOwner, "/", 2)
	if len(parts) != 2 {
		return fmt.Errorf("invalid repo format %q, expected owner/repo", nameWithOwner)
	}
	var repo *gh.Repository
	err := c.withRetry(fmt.Sprintf("fetching repo %s", nameWithOwner), func() (*gh.Response, error) {
		var (
			resp *gh.Response
			e    error
		)
		repo, resp, e = c.api.Repositories.Get(c.ctx, parts[0], parts[1])
		return resp, e
	})
	if err != nil {
		return fmt.Errorf("fetching repo %s: %w", nameWithOwner, err)
	}
	c.repo = repo
	return nil
}

// RepoFullName returns the "owner/name" of the configured repo.
func (c *Client) RepoFullName() string {
	if c.repo == nil {
		return ""
	}
	return c.repo.GetFullName()
}

// PRFile represents a file changed in a pull request.
type PRFile struct {
	Filename string
	Status   string
	Patch    string
}

// ListPRFiles fetches the files for a specific pull request.
func (c *Client) ListPRFiles(prNumber int) ([]PRFile, error) {
	if c.repo == nil {
		return nil, fmt.Errorf("repo not set; call SetRepo first")
	}

	owner := c.repo.GetOwner().GetLogin()
	name := c.repo.GetName()

	opts := &gh.ListOptions{PerPage: 100}
	var files []PRFile

	for {
		var ghFiles []*gh.CommitFile
		var resp *gh.Response
		err := c.withRetry(fmt.Sprintf("listing files for PR #%d", prNumber), func() (*gh.Response, error) {
			var e error
			ghFiles, resp, e = c.api.PullRequests.ListFiles(c.ctx, owner, name, prNumber, opts)
			return resp, e
		})
		if err != nil {
			return nil, err
		}

		for _, f := range ghFiles {
			files = append(files, PRFile{
				Filename: f.GetFilename(),
				Status:   f.GetStatus(),
				Patch:    f.GetPatch(),
			})
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return files, nil
}

// GetPRHeadFileContent fetches the content of a file at the PR's head commit.
func (c *Client) GetPRHeadFileContent(prNumber int, filename string) (string, error) {
	if c.repo == nil {
		return "", fmt.Errorf("repo not set; call SetRepo first")
	}
	owner := c.repo.GetOwner().GetLogin()
	name := c.repo.GetName()

	var pr *gh.PullRequest
	err := c.withRetry(fmt.Sprintf("getting PR #%d info", prNumber), func() (*gh.Response, error) {
		var (
			resp *gh.Response
			e    error
		)
		pr, resp, e = c.api.PullRequests.Get(c.ctx, owner, name, prNumber)
		return resp, e
	})
	if err != nil {
		return "", fmt.Errorf("getting PR #%d info: %w", prNumber, err)
	}

	opts := &gh.RepositoryContentGetOptions{
		Ref: pr.GetHead().GetSHA(),
	}

	var fileContent *gh.RepositoryContent
	err = c.withRetry(fmt.Sprintf("getting contents of %s for PR #%d", filename, prNumber), func() (*gh.Response, error) {
		var (
			resp *gh.Response
			e    error
		)
		fileContent, _, resp, e = c.api.Repositories.GetContents(c.ctx, owner, name, filename, opts)
		return resp, e
	})
	if err != nil {
		return "", err
	}

	if fileContent == nil {
		return "", fmt.Errorf("not a file")
	}

	return fileContent.GetContent()
}

// ProcessReviewThreads fetches conversation threads from merged PRs and calls
// onBatch for each PR's threads.
// trustedAuthors filters which comments to include (nil/empty = all).
//
// scanned maps PR numbers that were fully processed in previous runs to the
// PR's updatedAt at that time; such PRs are skipped (no comment fetch) when
// unchanged. watermark is the newest updatedAt covered by a previously
// completed run: once iteration (which is ordered by updatedAt descending)
// reaches a PR at or below it, the remaining history is already covered and
// pagination stops.
//
// It returns the newest PR updatedAt observed in this run so callers can
// persist it as the next watermark.
func (c *Client) ProcessReviewThreads(
	trustedAuthors map[string]bool,
	baseBranch string,
	minBodyLen int,
	existingIDs map[string]bool,
	scanned map[int]time.Time,
	watermark time.Time,
	onPR func(prNumber int, added int, updatedAt time.Time) error,
	onBatch func(threads []Thread) error,
) (time.Time, error) {
	var newest time.Time
	if c.repo == nil {
		return newest, fmt.Errorf("repo not set; call SetRepo first")
	}
	if baseBranch == "" {
		baseBranch = "master"
	}

	owner := c.repo.GetOwner().GetLogin()
	name := c.repo.GetName()

	opts := &gh.PullRequestListOptions{
		State:     "closed",
		Base:      baseBranch,
		Sort:      "updated",
		Direction: "desc",
		ListOptions: gh.ListOptions{
			PerPage: 30,
		},
	}

	for {
		var prs []*gh.PullRequest
		var resp *gh.Response
		err := c.withRetry("listing PRs", func() (*gh.Response, error) {
			var e error
			prs, resp, e = c.api.PullRequests.List(c.ctx, owner, name, opts)
			return resp, e
		})
		if err != nil {
			return newest, fmt.Errorf("listing PRs: %w", err)
		}

		for _, pr := range prs {
			updated := pr.GetUpdatedAt().Time
			if updated.After(newest) {
				newest = updated
			}

			// Stop once we cross into history fully covered by a previous run.
			if !watermark.IsZero() && !updated.After(watermark) {
				c.logDebug("  github: reached watermark %s at PR #%d; stopping pagination\n",
					watermark.Format(time.RFC3339), pr.GetNumber())
				return newest, nil
			}

			if pr.GetMergedAt().IsZero() {
				continue
			}

			prNum := pr.GetNumber()

			// Cheaply skip PRs already scanned and unchanged since then.
			if prev, ok := scanned[prNum]; ok && !updated.After(prev) {
				c.logDebug("  github: PR #%d already scanned and unchanged; skipping\n", prNum)
				continue
			}

			threads, err := c.fetchPRThreads(owner, name, prNum, trustedAuthors, minBodyLen, existingIDs, pr.GetHTMLURL())
			if err != nil {
				return newest, fmt.Errorf("fetching comments for PR #%d: %w", prNum, err)
			}

			if len(threads) > 0 {
				if err := onBatch(threads); err != nil {
					return newest, fmt.Errorf("processing batch for PR #%d: %w", prNum, err)
				}
			}

			if onPR != nil {
				if err := onPR(prNum, len(threads), updated); err != nil {
					return newest, err
				}
			}
		}

		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return newest, nil
}

// fetchPRThreads fetches all review comments for a PR and groups them
// into conversation threads using InReplyTo links.
func (c *Client) fetchPRThreads(
	owner, name string,
	prNumber int,
	trustedAuthors map[string]bool,
	minBodyLen int,
	existingIDs map[string]bool,
	prURL string,
) ([]Thread, error) {
	opts := &gh.PullRequestListCommentsOptions{
		ListOptions: gh.ListOptions{PerPage: 100},
	}

	// Collect all raw comments first.
	var allRaw []*gh.PullRequestComment
	for {
		var comments []*gh.PullRequestComment
		var resp *gh.Response
		err := c.withRetry(fmt.Sprintf("listing comments for PR #%d", prNumber), func() (*gh.Response, error) {
			var e error
			comments, resp, e = c.api.PullRequests.ListComments(c.ctx, owner, name, prNumber, opts)
			return resp, e
		})
		if err != nil {
			return nil, err
		}
		allRaw = append(allRaw, comments...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	if len(allRaw) == 0 {
		return nil, nil
	}

	// Index comments by ID for InReplyTo lookups.
	byID := make(map[int64]*gh.PullRequestComment, len(allRaw))
	for _, c := range allRaw {
		byID[c.GetID()] = c
	}

	// Group into threads: find root comment for each comment by following
	// InReplyTo chain, then group by root ID.
	rootOf := func(comment *gh.PullRequestComment) int64 {
		id := comment.GetID()
		seen := make(map[int64]bool)
		for {
			parent := comment.GetInReplyTo()
			if parent == 0 || seen[parent] {
				return id
			}
			seen[id] = true
			if p, ok := byID[parent]; ok {
				id = parent
				comment = p
			} else {
				return id
			}
		}
	}

	// threadMap: rootID -> list of raw comments
	threadMap := make(map[int64][]*gh.PullRequestComment)
	for _, c := range allRaw {
		root := rootOf(c)
		threadMap[root] = append(threadMap[root], c)
	}

	var result []Thread
	for rootID, rawComments := range threadMap {
		threadID := fmt.Sprintf("%d", rootID)

		// Skip if this thread is already in the DB.
		if existingIDs[threadID] {
			continue
		}

		// Sort comments by creation time for consistent ordering.
		sort.Slice(rawComments, func(i, j int) bool {
			return rawComments[i].GetCreatedAt().Before(rawComments[j].GetCreatedAt().Time)
		})

		// Get diff hunk and path from the root comment.
		rootComment := byID[rootID]
		hunk := rootComment.GetDiffHunk()
		if strings.TrimSpace(hunk) == "" {
			continue
		}

		// Build the thread comments, applying filters per comment.
		var threadComments []ThreadComment
		for _, rc := range rawComments {
			author := rc.GetUser().GetLogin()
			body := rc.GetBody()

			// Filter by author if specified.
			if len(trustedAuthors) > 0 && !trustedAuthors[author] {
				continue
			}
			// Filter by minimum body length.
			if len(body) <= minBodyLen {
				continue
			}

			threadComments = append(threadComments, ThreadComment{
				ID:        fmt.Sprintf("%d", rc.GetID()),
				Author:    author,
				Body:      body,
				CreatedAt: rc.GetCreatedAt().Format("2006-01-02T15:04:05Z"),
			})
		}

		// Only include threads that have at least one qualifying comment.
		if len(threadComments) == 0 {
			continue
		}

		result = append(result, Thread{
			ID:       threadID,
			DiffHunk: hunk,
			Path:     rootComment.GetPath(),
			PRURL:    prURL,
			Comments: threadComments,
		})
	}

	return result, nil
}

// Token tries, in order:
//  1. GITHUB_TOKEN env var
//  2. `gh auth token` command (works with keyring-backed gh installs)
//  3. Parsing ~/.config/gh/hosts.yml directly
func Token() (string, error) {
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		return tok, nil
	}
	if tok, err := ghAuthToken(); err == nil {
		return tok, nil
	}
	return readGHCLIToken()
}

// resolveToken is kept for backward compatibility with tests and NewClient.
func resolveToken() (string, error) {
	return Token()
}

// ghAuthToken shells out to `gh auth token`.
func ghAuthToken() (string, error) {
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("gh auth token returned empty")
	}
	return tok, nil
}

// ghHostsConfig is the structure of ~/.config/gh/hosts.yml
type ghHostsConfig map[string]struct {
	OAuthToken string `yaml:"oauth_token"`
	Token      string `yaml:"token"`
}

func readGHCLIToken() (string, error) {
	configDir := os.Getenv("GH_CONFIG_DIR")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("finding home dir: %w", err)
		}
		if runtime.GOOS == "windows" {
			configDir = filepath.Join(home, "AppData", "Roaming", "GitHub CLI")
		} else {
			configDir = filepath.Join(home, ".config", "gh")
		}
	}

	data, err := os.ReadFile(filepath.Join(configDir, "hosts.yml"))
	if err != nil {
		return "", fmt.Errorf("reading gh hosts.yml: %w (set GITHUB_TOKEN env var as alternative)", err)
	}

	var hosts ghHostsConfig
	if err := yaml.Unmarshal(data, &hosts); err != nil {
		return "", fmt.Errorf("parsing hosts.yml: %w", err)
	}

	for _, h := range hosts {
		if h.OAuthToken != "" {
			return h.OAuthToken, nil
		}
		if h.Token != "" {
			return h.Token, nil
		}
	}
	return "", fmt.Errorf("no token found in gh hosts.yml; set GITHUB_TOKEN instead")
}

// CreatePendingReview creates a pending review on the PR with the given comments.
func (c *Client) CreatePendingReview(prNumber int, comments []*gh.DraftReviewComment) error {
	if len(comments) == 0 {
		return nil
	}
	body := "ragrev automated review findings"
	req := &gh.PullRequestReviewRequest{
		Body:     &body,
		Comments: comments,
	}

	_, _, err := c.api.PullRequests.CreateReview(c.ctx, *c.repo.Owner.Login, *c.repo.Name, prNumber, req)
	return err
}
