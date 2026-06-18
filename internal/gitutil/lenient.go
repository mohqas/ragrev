package gitutil

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/cache"
	format "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// openRepoLenient opens the repository containing the current working directory
// while tolerating .git/config entries that go-git's strict parser rejects.
//
// go-git refuses to open a repository whose config has a branch with a `merge`
// value not under refs/heads/ (or an unrecognized `rebase` value), failing with
// errors like "branch config: invalid merge". Such configs are written by some
// tools yet accepted by real git. We open with a storage wrapper that returns a
// sanitized in-memory config, leaving the on-disk file untouched.
func openRepoLenient() (*git.Repository, error) {
	dotDir, err := locateGitDir()
	if err != nil {
		return nil, err
	}
	wtDir, err := TopLevelDir()
	if err != nil {
		// Fall back to the .git parent if the worktree root can't be resolved.
		wtDir = filepath.Dir(dotDir)
	}

	dot := osfs.New(dotDir)
	st := filesystem.NewStorage(dot, cache.NewObjectLRUDefault())
	storer := &lenientStorage{Storage: st}

	return git.Open(storer, osfs.New(wtDir))
}

// lenientStorage wraps a filesystem.Storage and sanitizes the git config so
// repositories with branch entries go-git considers invalid can still be opened.
type lenientStorage struct {
	*filesystem.Storage
}

// Config returns the repository config, repairing it in-memory if go-git's
// strict validation rejects the on-disk file.
func (s *lenientStorage) Config() (*gitconfig.Config, error) {
	cfg, err := s.Storage.Config()
	if err == nil {
		return cfg, nil
	}

	raw, rerr := s.readRawConfig()
	if rerr != nil {
		return nil, err // surface the original validation error
	}
	fixed, ferr := sanitizeGitConfig(raw)
	if ferr != nil {
		return nil, err
	}
	c := gitconfig.NewConfig()
	if uerr := c.Unmarshal(fixed); uerr != nil {
		return nil, err
	}
	return c, nil
}

// readRawConfig reads the raw bytes of the on-disk git config file.
func (s *lenientStorage) readRawConfig() ([]byte, error) {
	f, err := s.Filesystem().Open("config")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// sanitizeGitConfig drops branch options that go-git's parser rejects:
// a `merge` value not under refs/heads/, and any non-standard `rebase` value.
func sanitizeGitConfig(raw []byte) ([]byte, error) {
	c := format.New()
	if err := format.NewDecoder(bytes.NewReader(raw)).Decode(c); err != nil {
		return nil, err
	}

	if sec := c.Section("branch"); sec != nil {
		for _, sub := range sec.Subsections {
			if merge := sub.Option("merge"); merge != "" && !strings.HasPrefix(merge, "refs/heads/") {
				sub.RemoveOption("merge")
			}
			switch sub.Option("rebase") {
			case "", "true", "false", "interactive":
				// valid; leave untouched
			default:
				sub.RemoveOption("rebase")
			}
		}
	}

	var buf bytes.Buffer
	if err := format.NewEncoder(&buf).Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
