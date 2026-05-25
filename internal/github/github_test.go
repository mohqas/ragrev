package github

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// disableGHCLI sets PATH to an empty dir so `gh auth token` won't be found.
func disableGHCLI(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

func TestResolveToken_EnvVar(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "test-token-123")
	tok, err := resolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "test-token-123" {
		t.Errorf("got %q, want %q", tok, "test-token-123")
	}
}

func TestResolveToken_GHAuthToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	// This test only works when gh is installed and logged in.
	tok, err := ghAuthToken()
	if err != nil {
		t.Skipf("gh auth token not available: %v", err)
	}
	if tok == "" {
		t.Error("ghAuthToken returned empty string")
	}
}

func TestResolveToken_GHConfig(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	disableGHCLI(t)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)

	content := "github.com:\n    oauth_token: gh-config-token-456\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tok, err := resolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "gh-config-token-456" {
		t.Errorf("got %q, want %q", tok, "gh-config-token-456")
	}
}

func TestResolveToken_GHConfigTokenField(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	disableGHCLI(t)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)

	content := "github.com:\n    token: gh-new-token-789\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tok, err := resolveToken()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tok != "gh-new-token-789" {
		t.Errorf("got %q, want %q", tok, "gh-new-token-789")
	}
}

func TestResolveToken_NoToken(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "")
	disableGHCLI(t)
	dir := t.TempDir()
	t.Setenv("GH_CONFIG_DIR", dir)

	content := "github.com:\n    user: someone\n"
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := resolveToken()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFilterLogic(t *testing.T) {
	trusted := map[string]bool{"alice": true}
	minBodyLen := 20

	tests := []struct {
		name   string
		author string
		body   string
		hunk   string
		want   bool
	}{
		{"trusted with content", "alice", "this is a sufficiently long comment", "@@diff@@\n+foo", true},
		{"untrusted author", "bob", "this is a sufficiently long comment", "@@diff@@\n+foo", false},
		{"short body", "alice", "short", "@@diff@@\n+foo", false},
		{"empty hunk", "alice", "this is a sufficiently long comment", "   ", false},
		{"blank hunk", "alice", "this is a sufficiently long comment", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := trusted[tt.author] &&
				len(tt.body) > minBodyLen &&
				strings.TrimSpace(tt.hunk) != ""
			if got != tt.want {
				t.Errorf("filter = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestThreadAuthors(t *testing.T) {
	thread := Thread{
		ID:       "1",
		DiffHunk: "@@diff@@",
		Comments: []ThreadComment{
			{ID: "1", Author: "alice", Body: "first"},
			{ID: "2", Author: "bob", Body: "reply"},
			{ID: "3", Author: "alice", Body: "followup"},
		},
	}

	authors := thread.Authors()
	if len(authors) != 2 {
		t.Fatalf("expected 2 unique authors, got %d: %v", len(authors), authors)
	}
	if authors[0] != "alice" || authors[1] != "bob" {
		t.Errorf("expected [alice, bob], got %v", authors)
	}
}

func TestCommentsJSONRoundTrip(t *testing.T) {
	thread := Thread{
		ID: "1",
		Comments: []ThreadComment{
			{ID: "10", Author: "alice", Body: "good catch", CreatedAt: "2025-01-01T00:00:00Z"},
			{ID: "11", Author: "bob", Body: "thanks, fixed", CreatedAt: "2025-01-01T01:00:00Z"},
		},
	}

	jsonStr := thread.CommentsJSON()

	parsed, err := ParseCommentsJSON(jsonStr)
	if err != nil {
		t.Fatalf("ParseCommentsJSON: %v", err)
	}

	if len(parsed) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(parsed))
	}
	if parsed[0].Author != "alice" || parsed[1].Author != "bob" {
		t.Errorf("authors mismatch: %v", parsed)
	}
	if parsed[0].Body != "good catch" {
		t.Errorf("body mismatch: got %q", parsed[0].Body)
	}
}

func TestFilterCommentsByAuthors(t *testing.T) {
	comments := []ThreadComment{
		{ID: "1", Author: "alice", Body: "first"},
		{ID: "2", Author: "bob", Body: "second"},
		{ID: "3", Author: "charlie", Body: "third"},
		{ID: "4", Author: "alice", Body: "fourth"},
	}

	t.Run("nil authors returns all", func(t *testing.T) {
		result := FilterCommentsByAuthors(comments, nil)
		if len(result) != 4 {
			t.Errorf("expected 4, got %d", len(result))
		}
	})

	t.Run("empty authors returns all", func(t *testing.T) {
		result := FilterCommentsByAuthors(comments, map[string]bool{})
		if len(result) != 4 {
			t.Errorf("expected 4, got %d", len(result))
		}
	})

	t.Run("filter single author", func(t *testing.T) {
		result := FilterCommentsByAuthors(comments, map[string]bool{"alice": true})
		if len(result) != 2 {
			t.Errorf("expected 2, got %d", len(result))
		}
		for _, c := range result {
			if c.Author != "alice" {
				t.Errorf("expected alice, got %s", c.Author)
			}
		}
	})

	t.Run("filter team", func(t *testing.T) {
		result := FilterCommentsByAuthors(comments, map[string]bool{"alice": true, "charlie": true})
		if len(result) != 3 {
			t.Errorf("expected 3, got %d", len(result))
		}
	})
}

func TestParseCommentsJSON_Invalid(t *testing.T) {
	_, err := ParseCommentsJSON("not json")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}

func TestCommentsJSON_Encoding(t *testing.T) {
	// Ensure special characters in body are properly escaped
	thread := Thread{
		Comments: []ThreadComment{
			{ID: "1", Author: "alice", Body: `use "quotes" & <angle> brackets`},
		},
	}
	jsonStr := thread.CommentsJSON()

	var parsed []ThreadComment
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if parsed[0].Body != `use "quotes" & <angle> brackets` {
		t.Errorf("body mismatch after round-trip: %q", parsed[0].Body)
	}
}
