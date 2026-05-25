package gitutil

import (
	"testing"
)

func TestRepoBaseName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"owner/repo", "repo"},
		{"canonical/chisel", "chisel"},
		{"justname", "justname"},
	}
	for _, tt := range tests {
		got := RepoBaseName(tt.input)
		if got != tt.want {
			t.Errorf("RepoBaseName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestDetectRepo(t *testing.T) {
	// This test only works inside a git repo with a GitHub origin.
	// Skip gracefully if not.
	repo, err := DetectRepo()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	if repo == "" {
		t.Error("DetectRepo returned empty string")
	}
	// Should be in owner/repo format
	if len(repo) < 3 {
		t.Errorf("DetectRepo returned suspiciously short: %q", repo)
	}
}

func TestGitDir(t *testing.T) {
	dir, err := GitDir()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	if dir == "" {
		t.Error("GitDir returned empty string")
	}
}

func TestDBPath(t *testing.T) {
	p, err := DBPath()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	if p == "" {
		t.Error("DBPath returned empty string")
	}
}

