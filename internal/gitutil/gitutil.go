// Package gitutil provides helpers for detecting git repo info from the working
// directory. It uses the pure-Go go-git library (no external git binary), which
// keeps ragrev usable inside a strictly confined snap.
package gitutil

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/utils/merkletrie"
)

// repoPattern matches GitHub owner/repo from various remote URL formats.
var repoPattern = regexp.MustCompile(`github\.com[:/]([^/]+/[^/.\s]+?)(?:\.git)?$`)

// openRepo opens the git repository containing the current working directory,
// walking up parent directories to find the .git folder.
func openRepo() (*git.Repository, error) {
	repo, err := git.PlainOpenWithOptions(".", &git.PlainOpenOptions{DetectDotGit: true})
	if err == nil {
		return repo, nil
	}

	// go-git strictly validates .git/config and refuses to open repositories
	// whose branch entries it considers invalid (e.g. "branch config: invalid
	// merge"). Retry with a storage wrapper that sanitizes the config in-memory.
	if repo, lerr := openRepoLenient(); lerr == nil {
		return repo, nil
	}

	return nil, fmt.Errorf("not inside a git repository: %w", err)
}

// resolveHash resolves a revision string (full/abbrev SHA, ref, etc.) to a hash.
func resolveHash(repo *git.Repository, rev string) (plumbing.Hash, error) {
	h, err := repo.ResolveRevision(plumbing.Revision(rev))
	if err != nil {
		return plumbing.ZeroHash, fmt.Errorf("resolving revision %q: %w", rev, err)
	}
	return *h, nil
}

// DetectRepo returns the "owner/repo" for the GitHub remote of the
// git repository in the current (or nearest parent) directory.
func DetectRepo() (string, error) {
	repo, err := openRepo()
	if err != nil {
		return "", fmt.Errorf("not a git repo: %w", err)
	}
	rem, err := repo.Remote("origin")
	if err != nil {
		return "", fmt.Errorf("not a git repo or no 'origin' remote: %w", err)
	}
	urls := rem.Config().URLs
	if len(urls) == 0 {
		return "", fmt.Errorf("'origin' remote has no URL configured")
	}
	url := strings.TrimSpace(urls[0])
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

// locateGitDir walks up from the working directory to find the .git directory,
// resolving the "gitdir:" pointer when .git is a file (worktrees/submodules).
func locateGitDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		gitPath := filepath.Join(dir, ".git")
		info, err := os.Stat(gitPath)
		if err == nil {
			if info.IsDir() {
				return filepath.Abs(gitPath)
			}
			// .git is a file pointing at the real git dir.
			data, rerr := os.ReadFile(gitPath)
			if rerr == nil {
				line := strings.TrimSpace(string(data))
				if p, ok := strings.CutPrefix(line, "gitdir:"); ok {
					p = strings.TrimSpace(p)
					if !filepath.IsAbs(p) {
						p = filepath.Join(dir, p)
					}
					return filepath.Clean(p), nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("not inside a git repository")
}

// GitDir returns the path to the .git directory for the current repo.
func GitDir() (string, error) {
	return locateGitDir()
}

// TopLevelDir returns the absolute path to the root working directory of the git repo.
func TopLevelDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return filepath.Abs(dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("not inside a git repository")
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

// GlobalDir returns the per-user ragrev configuration directory.
//
// Under a strictly confined snap, $SNAP_USER_COMMON is used — it is writable
// without any extra interface plugs and survives snap refreshes.
// Otherwise the XDG-compliant os.UserConfigDir()/ragrev (~/.config/ragrev) is
// used, with $HOME/.ragrev as a final fallback.
func GlobalDir() string {
	if snap := os.Getenv("SNAP_USER_COMMON"); snap != "" {
		return snap
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "ragrev")
	}
	if home := os.Getenv("HOME"); home != "" {
		return filepath.Join(home, ".ragrev")
	}
	return ".ragrev"
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
	repo, err := openRepo()
	if err != nil {
		return nil, err
	}
	head, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("getting current branch: %w", err)
	}
	if !head.Name().IsBranch() {
		return nil, fmt.Errorf("HEAD is detached; cannot determine upstream")
	}
	branch := head.Name().Short()
	cfg, err := repo.Config()
	if err != nil {
		return nil, fmt.Errorf("reading git config: %w", err)
	}
	br, ok := cfg.Branches[branch]
	if !ok || br.Remote == "" {
		return nil, fmt.Errorf("no upstream branch configured for %s", branch)
	}
	// The remote-tracking ref, e.g. refs/remotes/origin/<branch>.
	remoteBranch := branch
	if br.Merge != "" {
		remoteBranch = br.Merge.Short()
	}
	upRef, err := repo.Reference(plumbing.NewRemoteReferenceName(br.Remote, remoteBranch), true)
	if err != nil {
		return nil, fmt.Errorf("no upstream branch configured for %s", branch)
	}
	upHash := upRef.Hash()
	iter, err := repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, fmt.Errorf("listing unpushed commits: %w", err)
	}
	defer iter.Close()
	var commits []string
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash == upHash {
			return storer.ErrStop
		}
		commits = append(commits, c.Hash.String())
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing unpushed commits: %w", err)
	}
	// git log yields newest-first; return chronological (oldest-first) order so
	// callers can use commits[0] as the base anchor and commits[len-1] as HEAD.
	for i := 0; i < len(commits)/2; i++ {
		j := len(commits) - 1 - i
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}

// ChangedFiles returns the list of files changed in the given commits with their patches.
func ChangedFiles(commits []string) ([]ChangedFile, error) {
	if len(commits) == 0 {
		return nil, fmt.Errorf("no commits specified")
	}
	repo, err := openRepo()
	if err != nil {
		return nil, err
	}
	firstHash, err := resolveHash(repo, commits[0])
	if err != nil {
		return nil, err
	}
	lastHash, err := resolveHash(repo, commits[len(commits)-1])
	if err != nil {
		return nil, err
	}
	firstCommit, err := repo.CommitObject(firstHash)
	if err != nil {
		return nil, fmt.Errorf("loading commit %s: %w", commits[0], err)
	}
	lastCommit, err := repo.CommitObject(lastHash)
	if err != nil {
		return nil, fmt.Errorf("loading commit %s: %w", commits[len(commits)-1], err)
	}
	// Base tree is the parent of the first commit (i.e. commits[0]~1),
	// matching the diff range first~1..last.
	var baseTree *object.Tree
	if firstCommit.NumParents() > 0 {
		parent, err := firstCommit.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("loading parent of %s: %w", commits[0], err)
		}
		if baseTree, err = parent.Tree(); err != nil {
			return nil, err
		}
	}
	lastTree, err := lastCommit.Tree()
	if err != nil {
		return nil, err
	}
	changes, err := object.DiffTree(baseTree, lastTree)
	if err != nil {
		return nil, fmt.Errorf("computing diff: %w", err)
	}
	var files []ChangedFile
	for _, ch := range changes {
		action, err := ch.Action()
		if err != nil {
			continue
		}
		var status, filename string
		switch action {
		case merkletrie.Insert:
			status = "added"
			filename = ch.To.Name
		case merkletrie.Delete:
			status = "removed"
			filename = ch.From.Name
		default:
			status = "modified"
			filename = ch.To.Name
		}
		patch := ""
		if p, err := ch.Patch(); err == nil {
			patch = p.String()
		}
		files = append(files, ChangedFile{
			Filename: filename,
			Status:   status,
			Patch:    patch,
		})
	}
	return files, nil
}

// FileContentAtCommit returns the content of a file at a specific commit.
func FileContentAtCommit(commit, path string) (string, error) {
	repo, err := openRepo()
	if err != nil {
		return "", err
	}
	hash, err := resolveHash(repo, commit)
	if err != nil {
		return "", err
	}
	commitObj, err := repo.CommitObject(hash)
	if err != nil {
		return "", fmt.Errorf("loading commit %s: %w", commit, err)
	}
	f, err := commitObj.File(path)
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", path, commit, err)
	}
	content, err := f.Contents()
	if err != nil {
		return "", fmt.Errorf("reading %s at %s: %w", path, commit, err)
	}
	return content, nil
}

// ExpandCommitRange expands a commit range (start:end) into a list of commit SHAs,
// in chronological order from start to end (inclusive).
func ExpandCommitRange(start, end string) ([]string, error) {
	repo, err := openRepo()
	if err != nil {
		return nil, err
	}
	startHash, err := resolveHash(repo, start)
	if err != nil {
		return nil, err
	}
	endHash, err := resolveHash(repo, end)
	if err != nil {
		return nil, err
	}
	iter, err := repo.Log(&git.LogOptions{From: endHash})
	if err != nil {
		return nil, fmt.Errorf("expanding commit range %s..%s: %w", start, end, err)
	}
	defer iter.Close()
	// Collect commits from end back to (but excluding) start; newest first.
	var commits []string
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash == startHash {
			return storer.ErrStop
		}
		commits = append(commits, c.Hash.String())
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("expanding commit range %s..%s: %w", start, end, err)
	}
	// Add the start commit.
	commits = append(commits, startHash.String())
	// Reverse to get chronological order.
	for i := 0; i < len(commits)/2; i++ {
		j := len(commits) - 1 - i
		commits[i], commits[j] = commits[j], commits[i]
	}
	return commits, nil
}
