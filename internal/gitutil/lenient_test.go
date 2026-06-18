package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestOpenRepoToleratesInvalidBranchMerge ensures ragrev can open repositories
// whose .git/config contains a branch `merge` value that go-git's strict parser
// rejects (e.g. one not under refs/heads/). Real git accepts such configs, and
// some tooling writes them, so openRepo must fall back to a sanitized open
// instead of failing with "branch config: invalid merge".
func TestOpenRepoToleratesInvalidBranchMerge(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}

	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		)
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	run("init", "-q")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hi\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-qm", "c1")

	// Append a branch entry with an invalid (non refs/heads/) merge value.
	cfgPath := filepath.Join(dir, ".git", "config")
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n[branch \"weird\"]\n\tremote = origin\n\tmerge = master\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	wd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(wd) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	repo, err := openRepo()
	if err != nil {
		t.Fatalf("openRepo should tolerate invalid branch merge, got: %v", err)
	}
	if repo == nil {
		t.Fatal("expected a repository, got nil")
	}
}
