// Package config manages ragrev's per-repo YAML configuration file.
// The config lives inside .git/ragrev/config.yml so it is automatically
// git-ignored and specific to each checkout.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// BackendConfig holds per-provider LLM settings.
type BackendConfig struct {
	Model    string `yaml:"model"`
	URL      string `yaml:"url"`
	TokenEnv string `yaml:"token_env,omitempty"`
	TokenCmd string `yaml:"token_cmd,omitempty"`
	// MaxPromptTokens is the maximum prompt size (in tokens) ragrev will
	// send to this backend. When 0, a sensible per-model default (~75%
	// of the model's advertised context window) is used.
	MaxPromptTokens int `yaml:"max_prompt_tokens,omitempty"`
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
	// MaxExamples is the maximum number of similar past review threads to include per chunk.
	MaxExamples int `yaml:"max_examples,omitempty"`
	// MinSimilarityScore is the minimum vector-similarity score (0–1) a DB
	// result must reach before its comments are included as review context.
	MinSimilarityScore float64 `yaml:"min_similarity_score,omitempty"`
	// MinCommentLen is the minimum comment body length to include.
	MinCommentLen int `yaml:"min_comment_len"`
	// RateDelay is the delay between processing PRs.
	RateDelay Duration `yaml:"rate_delay"`

	// Backend selects which LLM backend to use (e.g. "github", "gemini", "ollama").
	Backend string `yaml:"backend,omitempty"`
	// Backends holds configuration for each individual LLM provider.
	Backends map[string]BackendConfig `yaml:"backends,omitempty"`

	// ReviewScope controls how much PR context is preloaded into the LLM
	// conversation before chunk-level review. One of:
	//   - "pr"    : preload every changed file's full content up front (default)
	//   - "file"  : inject each file's full content lazily when its first chunk is reviewed
	//   - "chunk" : send only the chunk diff plus the relevant file content per call,
	//               discarding history between chunks
	ReviewScope string `yaml:"review_scope,omitempty"`

	// ReviewPrompt overrides the built-in review system instructions when
	// non-empty. The current-date prefix is always retained, but the default
	// snippet/LGTM instructions are dropped, which yields larger output and
	// less accurate comment locations. Empty (default) keeps the built-in
	// prompt. Use with caution.
	ReviewPrompt string `yaml:"review_prompt,omitempty"`
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
		BaseBranch:         "master",
		MinCommentLen:      10,
		MaxExamples:        2,
		MinSimilarityScore: 0.5,
		RateDelay:          Duration(100 * time.Millisecond),
		ReviewScope:        "pr",
		Backend:            "copilot",
		Backends: map[string]BackendConfig{
			"github": {
				Model:    "gpt-4o-mini",
				URL:      "https://models.inference.ai.azure.com/chat/completions",
				TokenEnv: "GITHUB_TOKEN",
				TokenCmd: "gh auth token",
			},
			"copilot": {
				Model:    "claude-sonnet-4.6",
				URL:      "https://api.githubcopilot.com/chat/completions",
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
	if cfg.ReviewScope == "" {
		cfg.ReviewScope = defaults.ReviewScope
	}
	if cfg.MinSimilarityScore == 0 {
		cfg.MinSimilarityScore = defaults.MinSimilarityScore
	}
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
// The generated YAML is annotated with comments describing each field so
// users can discover and tune options directly from the file.
func Save(ragrevDir string, cfg Config) error {
	if err := os.MkdirAll(ragrevDir, 0755); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := marshalWithComments(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	p := Path(ragrevDir)
	header := "# ragrev configuration — edit with: ragrev config\n" +
		"# Comments below describe each option; values shown are the active defaults.\n\n"

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

// knownTopLevelKeys returns the set of YAML key names defined by the Config
// struct, derived from struct field tags so it stays in sync automatically.
func knownTopLevelKeys() map[string]struct{} {
	t := reflect.TypeOf(Config{})
	keys := make(map[string]struct{}, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("yaml")
		if tag == "" || tag == "-" {
			continue
		}
		name := strings.SplitN(tag, ",", 2)[0]
		keys[name] = struct{}{}
	}
	return keys
}

// Update loads an existing config file, merges in any new default values
// (added in newer ragrev versions), and re-writes the file so the user's
// copy always stays up-to-date.  Unknown keys that are no longer part of
// the Config schema are reported as warnings but do not block the update.
// If the config file does not exist it is created with defaults (same
// behaviour as EnsureExists for a first run).
//
// Returns the merged Config, a (possibly empty) list of human-readable
// warnings about unrecognised/removed fields, and any hard error.
func Update(ragrevDir string) (Config, []string, error) {
	p := Path(ragrevDir)

	data, readErr := os.ReadFile(p)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			// First run — create with defaults, no warnings.
			cfg := Default()
			if err := Save(ragrevDir, cfg); err != nil {
				return cfg, nil, err
			}
			return cfg, nil, nil
		}
		return Config{}, nil, fmt.Errorf("reading config: %w", readErr)
	}

	// Detect unknown / removed top-level keys.
	var warnings []string
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err == nil {
		known := knownTopLevelKeys()
		var unknown []string
		for key := range raw {
			if _, ok := known[key]; !ok {
				unknown = append(unknown, key)
			}
		}
		sort.Strings(unknown)
		for _, key := range unknown {
			warnings = append(warnings, fmt.Sprintf("config field %q is no longer recognised and will be ignored", key))
		}
	}

	// Load with defaults merged in.
	cfg, err := Load(ragrevDir)
	if err != nil {
		return cfg, warnings, err
	}

	// Re-save so any newly added default fields appear in the file.
	if err := Save(ragrevDir, cfg); err != nil {
		return cfg, warnings, fmt.Errorf("updating config: %w", err)
	}

	return cfg, warnings, nil
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

	var args []string
	if editor == "" {
		// Inside a confined snap, prefer the editor bundled with the snap so
		// the command works out of the box without relying on host binaries.
		if snap := os.Getenv("SNAP"); snap != "" {
			if nano := filepath.Join(snap, "usr", "bin", "nano"); fileExists(nano) {
				editor = nano
				// -I stops nano from reading the base/host /etc/nanorc, which
				// AppArmor denies inside the confined snap.
				args = append(args, "-I")
			}
		}
	}
	if editor == "" {
		editor = "nano"
	}

	args = append(args, p)
	cmd := exec.Command(editor, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// fileExists reports whether the given path exists and is a regular file.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
