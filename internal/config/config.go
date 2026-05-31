// Package config manages ragrev's per-repo YAML configuration file.
// The config lives inside .git/ragrev/config.yml so it is automatically
// git-ignored and specific to each checkout.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// BackendConfig holds per-provider LLM settings.
type BackendConfig struct {
	Model    string `yaml:"model"`
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env,omitempty"`
	TokenCmd string `yaml:"token_cmd,omitempty"`
}

// Config holds all shared settings used by both generate and review commands.
type Config struct {
	// Repo is the GitHub "owner/repo" string.
	Repo string `yaml:"repo,omitempty"`
	// BaseBranch is the target branch for PR lookups (default: master).
	BaseBranch string `yaml:"base_branch"`
	// Authors is the list of trusted reviewer usernames.
	// Empty means all authors.
	Authors []string `yaml:"authors,omitempty"`
	// Reviewers is the list of usernames to filter by at review time.
	Reviewers []string `yaml:"reviewers,omitempty"`
	// MinCommentLen is the minimum comment body length to include.
	MinCommentLen int `yaml:"min_comment_len"`
	// RateDelay is the delay between processing PRs.
	RateDelay Duration `yaml:"rate_delay"`

	// Backend selects which LLM backend to use (e.g. "github", "gemini", "ollama").
	Backend string `yaml:"backend,omitempty"`
	// Backends holds configuration for each individual LLM provider.
	Backends map[string]BackendConfig `yaml:"backends,omitempty"`
}

// Duration wraps time.Duration for YAML marshaling as a human-readable string.
type Duration time.Duration

func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}
	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}
	*d = Duration(dur)
	return nil
}

// Default returns a Config with sensible defaults.
func Default() Config {
	return Config{
		BaseBranch:    "master",
		MinCommentLen: 10,
		RateDelay:     Duration(1 * time.Second),
		Backend:       "github",
		Backends: map[string]BackendConfig{
			"github": {
				Model:    "gpt-4o-mini",
				URL:      "https://models.inference.ai.azure.com/chat/completions",
				TokenEnv: "GITHUB_TOKEN",
				TokenCmd: "gh auth token",
			},
			"gemini": {
				Model:    "gemini-2.0-flash",
				URL:      "https://generativelanguage.googleapis.com/v1beta",
				TokenEnv: "GEMINI_API_KEY",
				TokenCmd: "gcloud auth print-access-token",
			},
			"ollama": {
				Model: "llama3",
				URL:   "http://localhost:11434/v1/chat/completions",
			},
		},
	}
}

// configFileName is the name of the config file inside the ragrev data dir.
const configFileName = "config.yml"

// Path returns the absolute path to the config file for the current repo.
// The file lives at .git/ragrev/config.yml.
func Path(ragrevDir string) string {
	return filepath.Join(ragrevDir, configFileName)
}

// Load reads the config from disk. If the file doesn't exist, it returns
// the default config (and does NOT create the file).
func Load(ragrevDir string) (Config, error) {
	cfg := Default()
	p := Path(ragrevDir)

	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config %s: %w", p, err)
	}

	// Merge back missing default backends or backend properties
	defaults := Default()
	if cfg.Backends == nil {
		cfg.Backends = make(map[string]BackendConfig)
	}
	for name, defaultBackend := range defaults.Backends {
		if existing, ok := cfg.Backends[name]; !ok {
			// Entire backend is missing, copy default
			cfg.Backends[name] = defaultBackend
		} else {
			// Backend exists, merge missing properties
			changed := false
			if existing.Model == "" {
				existing.Model = defaultBackend.Model
				changed = true
			}
			if existing.URL == "" {
				existing.URL = defaultBackend.URL
				changed = true
			}
			if existing.TokenEnv == "" {
				existing.TokenEnv = defaultBackend.TokenEnv
				changed = true
			}
			if existing.TokenCmd == "" {
				existing.TokenCmd = defaultBackend.TokenCmd
				changed = true
			}
			if changed {
				cfg.Backends[name] = existing
			}
		}
	}

	return cfg, nil
}

// Save writes the config to disk, creating the directory if needed.
func Save(ragrevDir string, cfg Config) error {
	if err := os.MkdirAll(ragrevDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	p := Path(ragrevDir)
	header := "# ragrev configuration — edit with: ragrev config\n" +
		"# This file is stored inside .git/ and is not tracked by git.\n\n"

	if err := os.WriteFile(p, []byte(header+string(data)), 0644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

// EnsureExists creates the config file with defaults if it doesn't exist yet.
// Returns the config and the path.
func EnsureExists(ragrevDir string) (Config, string, error) {
	p := Path(ragrevDir)
	if _, err := os.Stat(p); err == nil {
		cfg, err := Load(ragrevDir)
		return cfg, p, err
	}

	cfg := Default()
	if err := Save(ragrevDir, cfg); err != nil {
		return cfg, p, err
	}
	return cfg, p, nil
}

// Edit opens the config file in the user's default editor.
func Edit(ragrevDir string) error {
	// Ensure config file exists before opening editor
	_, p, err := EnsureExists(ragrevDir)
	if err != nil {
		return err
	}

	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = os.Getenv("VISUAL")
	}
	if editor == "" {
		editor = "vi"
	}

	cmd := exec.Command(editor, p)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
