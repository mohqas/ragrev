package cmd

import (
	"fmt"

	"github.com/canonical/ragrev/internal/config"
	"github.com/canonical/ragrev/internal/gitutil"
)

// ragrevDir returns the .git/ragrev path, used for both config and DB storage.
func ragrevDir() (string, error) {
	return gitutil.DBPath()
}

// loadConfig loads the config file from .git/ragrev/config.yml,
// auto-detecting repo if not already set, and saves back if it
// filled in any auto-detected values.
func loadConfig() (config.Config, string, error) {
	dir, err := ragrevDir()
	if err != nil {
		return config.Config{}, "", fmt.Errorf("could not find .git directory: %w", err)
	}

	cfg, _, err := config.EnsureExists(dir)
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
