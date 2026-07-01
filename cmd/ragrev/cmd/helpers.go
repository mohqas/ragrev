package cmd

import (
	"fmt"

	"github.com/canonical/ragrev/internal/config"
	"github.com/canonical/ragrev/internal/gitutil"
)

// init wires the config package's base-branch detector to the local git
// remote lookup. Detection then happens once, when a per-repo config is first
// created, and the resolved branch is persisted into the config file.
func init() {
	config.BaseBranchDetector = gitutil.DetectBaseBranch
}

// ragrevDir returns the .git/ragrev path, used for both config and DB storage.
func ragrevDir() (string, error) {
	return gitutil.DBPath()
}

// globalDir returns the per-user ragrev config directory (snap-safe).
func globalDir() string {
	return gitutil.GlobalDir()
}

// loadConfig loads the config file from .git/ragrev/config.yml,
// auto-detecting repo if not already set, and saves back if it
// filled in any auto-detected values.
// When creating the repo config for the first time it is seeded with
// any values already present in the global config.
func loadConfig() (config.Config, string, error) {
	dir, err := ragrevDir()
	if err != nil {
		return config.Config{}, "", fmt.Errorf("could not find .git directory: %w", err)
	}

	cfg, _, err := config.EnsureExistsSeeded(dir, globalDir())
	if err != nil {
		return cfg, dir, fmt.Errorf("cannot access repo data at %s; under the confined snap the repo "+
			"must be in your home directory or on removable media (try: snap connect ragrev:removable-media): %w", dir, err)
	}

	changed := false

	// Auto-detect repo if not set in config
	if cfg.Repo == "" {
		repo, err := gitutil.DetectRepo()
		if err != nil {
			return cfg, dir, fmt.Errorf("could not detect repo (set it in config or use --repo): %w", err)
		}
		cfg.Repo = repo
		changed = true
	}

	if changed {
		_ = config.Save(dir, cfg)
	}

	return cfg, dir, nil
}
