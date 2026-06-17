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

// ChangedFile represents a file changed in a git commit or range.
type ChangedFile struct {
	Filename string
	Status   string // "added", "modified", "removed", "renamed"
	Patch    string
}

// UnpushedCommits returns the list of commit SHAs that exist locally
// but have not been pushed to the upstream branch.
func UnpushedCommits() ([]string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("getting current branch: %w", err)
	}
	branch := strings.TrimSpace(string(out))

	// Check if upstream exists
	_, err = exec.Command("git", "rev-parse", "--abbrev-ref", branch+"@{upstream}").Output()
	if err != nil {
		return nil, fmt.Errorf("no upstream branch configured for %s", branch)
	}

	out, err = exec.Command("git", "log", branch+"@{upstream}..HEAD", "--format=%H").Output()
	if err != nil {
		return nil, fmt.Errorf("listing unpushed commits: %w", err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}

	return commits, nil
}

// ChangedFiles returns the list of files changed in the given commits with their patches.
func ChangedFiles(commits []string) ([]ChangedFile, error) {
	if len(commits) == 0 {
		return nil, fmt.Errorf("no commits specified")
	}

	// Build the range for diff
	var diffRange string
	if len(commits) == 1 {
		diffRange = commits[0] + "~1.." + commits[0]
	} else {
		// For multiple commits, use first~1..last
		diffRange = commits[0] + "~1.." + commits[len(commits)-1]
	}

	// Get the list of changed files with their status
	out, err := exec.Command("git", "diff", "--name-status", diffRange).Output()
	if err != nil {
		return nil, fmt.Errorf("getting changed files: %w", err)
	}

	var files []ChangedFile
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		status := parts[0]
		filename := parts[1]

		// Map git status codes to descriptive strings
		var statusStr string
		switch {
		case strings.HasPrefix(status, "A"):
			statusStr = "added"
		case strings.HasPrefix(status, "M"):
			statusStr = "modified"
		case strings.HasPrefix(status, "D"):
			statusStr = "removed"
		case strings.HasPrefix(status, "R"):
			statusStr = "renamed"
			if len(parts) >= 3 {
				filename = parts[2]
			}
		default:
			statusStr = "modified"
		}

		// Get the patch for this file
		patchOut, err := exec.Command("git", "diff", diffRange, "--", filename).Output()
		patch := ""
		if err == nil {
			patch = string(patchOut)
		}

		files = append(files, ChangedFile{
			Filename: filename,
			Status:   statusStr,
			Patch:    patch,
		})
	}

	return files, nil
}

// FileContentAtCommit returns the content of a file at a specific commit.
func FileContentAtCommit(commit, path string) (string, error) {
	out, err := exec.Command("git", "show", commit+":"+path).Output()
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", path, commit, err)
	}
	return string(out), nil
}

// ExpandCommitRange expands a commit range (start:end) into a list of commit SHAs.
func ExpandCommitRange(start, end string) ([]string, error) {
	out, err := exec.Command("git", "rev-list", "--ancestry-path", start+".."+end).Output()
	if err != nil {
		return nil, fmt.Errorf("expanding commit range %s..%s: %w", start, end, err)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var commits []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			commits = append(commits, line)
		}
	}

	// Add the start commit
	commits = append(commits, start)

	// Reverse to get chronological order
	for i := 0; i < len(commits)/2; i++ {
		j := len(commits) - 1 - i
		commits[i], commits[j] = commits[j], commits[i]
	}

	return commits, nil
}
