// Package gitutil provides helpers for detecting git repo info from the working directory.
package gitutil

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// repoPattern matches GitHub owner/repo from various remote URL formats.
var repoPattern = regexp.MustCompile(`github\.com[:/]([^/]+/[^/.\s]+?)(?:\.git)?$`)

// DetectRepo returns the "owner/repo" for the GitHub remote of the
// git repository in the current (or nearest parent) directory.
func DetectRepo() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("not a git repo or no 'origin' remote: %w", err)
	}
	url := strings.TrimSpace(string(out))
	m := repoPattern.FindStringSubmatch(url)
	if m == nil {
		return "", fmt.Errorf("could not parse GitHub owner/repo from remote URL %q", url)
	}
	return m[1], nil
}

// RepoBaseName returns the repo name (without owner) from an "owner/repo" string.
func RepoBaseName(nameWithOwner string) string {
	parts := strings.SplitN(nameWithOwner, "/", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return nameWithOwner
}

// GitDir returns the path to the .git directory for the current repo.
func GitDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--git-dir").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// TopLevelDir returns the absolute path to the root working directory of the git repo.
func TopLevelDir() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git repository: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	return abs, nil
}

// DBPath returns the default ragrev DB path inside the .git directory.
// Data stored here is automatically git-ignored.
func DBPath() (string, error) {
	gitDir, err := GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "ragrev"), nil
}
