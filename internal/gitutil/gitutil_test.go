package gitutil

import (
	"strings"
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

func TestDetectBaseBranch(t *testing.T) {
	// Only works inside a git repo whose origin/HEAD symbolic ref is set.
	// Skip gracefully otherwise (e.g. shallow CI checkouts).
	branch, err := DetectBaseBranch()
	if err != nil {
		t.Skipf("skipping: %v", err)
	}
	if branch == "" {
		t.Error("DetectBaseBranch returned empty string with no error")
	}
	if strings.HasPrefix(branch, "origin/") {
		t.Errorf("DetectBaseBranch should strip the remote prefix, got %q", branch)
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
