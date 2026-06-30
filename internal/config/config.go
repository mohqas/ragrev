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

	// OutputTheme is the Chroma syntax-highlight theme used for rich CLI
	// output. Any theme name listed at https://xyproto.github.io/splash/docs/all.html
	// is accepted. Defaults to "github".
	OutputTheme string `yaml:"output_theme,omitempty"`
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
		OutputTheme:        "github",
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
			"openrouter": {
				Model:    "meta-llama/llama-3.3-70b-instruct:free",
				URL:      "https://openrouter.ai/api/v1/chat/completions",
				TokenEnv: "OPENROUTER_API_KEY",
			},
		},
	}
}

// configFileName is the name of the config file inside the ragrev data dir.
const configFileName = "config.yml"

// ParseBackendName splits a backend key into its client type and optional
// alias. Keys may use the form "client(alias)" to define multiple presets
// that share the same underlying client implementation — for example
// "github(opus)" and "github(gemini)" both route through the github client
// but can carry different models. When the key has no parentheses the whole
// string is the client type and the alias is empty (backwards compatible).
func ParseBackendName(name string) (client, alias string) {
	open := strings.IndexByte(name, '(')
	if open <= 0 || !strings.HasSuffix(name, ")") {
		return name, ""
	}
	return name[:open], name[open+1 : len(name)-1]
}

// KnownClients returns the sorted list of built-in backend client types.
// These are the prefixes accepted before the "(alias)" portion of a backend
// key, and the only clients NewProvider knows how to construct.
func KnownClients() []string {
	defs := Default().Backends
	clients := make([]string, 0, len(defs))
	for name := range defs {
		clients = append(clients, name)
	}
	sort.Strings(clients)
	return clients
}

// mergeBackendDefaults fills any empty field of existing with the value from
// def, returning the merged result. It never overwrites a field the user has
// already set.
func mergeBackendDefaults(existing, def BackendConfig) BackendConfig {
	if existing.Model == "" {
		existing.Model = def.Model
	}
	if existing.URL == "" {
		existing.URL = def.URL
	}
	if existing.TokenEnv == "" {
		existing.TokenEnv = def.TokenEnv
	}
	if existing.TokenCmd == "" {
		existing.TokenCmd = def.TokenCmd
	}
	return existing
}

// Path returns the absolute path to the config file for the given ragrev dir.
func Path(ragrevDir string) string {
	return filepath.Join(ragrevDir, configFileName)
}

// GlobalPath returns the absolute path to the global config file.
// The filename is identical to the per-repo one; only the directory differs.
func GlobalPath(globalDir string) string {
	return filepath.Join(globalDir, configFileName)
}

// ApplyGlobal overlays the non-repo-specific fields of global onto base.
// The fields Repo, BaseBranch, and Reviewers are intentionally skipped
// because they are inherently per-repository.  All other fields are taken
// from global, with Backends deep-merged so per-backend user customisations
// in global are applied without wiping repo-level backend overrides in base.
func ApplyGlobal(base Config, global Config) Config {
	base.Authors = global.Authors
	base.MaxExamples = global.MaxExamples
	base.MinSimilarityScore = global.MinSimilarityScore
	base.MinCommentLen = global.MinCommentLen
	base.RateDelay = global.RateDelay
	base.Backend = global.Backend
	base.ReviewScope = global.ReviewScope
	base.ReviewPrompt = global.ReviewPrompt
	base.OutputTheme = global.OutputTheme

	// Deep-merge backends: global values fill in missing per-backend fields.
	if global.Backends != nil {
		if base.Backends == nil {
			base.Backends = make(map[string]BackendConfig)
		}
		for name, gb := range global.Backends {
			if existing, ok := base.Backends[name]; !ok {
				base.Backends[name] = gb
			} else {
				if gb.Model != "" {
					existing.Model = gb.Model
				}
				if gb.URL != "" {
					existing.URL = gb.URL
				}
				if gb.TokenEnv != "" {
					existing.TokenEnv = gb.TokenEnv
				}
				if gb.TokenCmd != "" {
					existing.TokenCmd = gb.TokenCmd
				}
				if gb.MaxPromptTokens != 0 {
					existing.MaxPromptTokens = gb.MaxPromptTokens
				}
				base.Backends[name] = existing
			}
		}
	}
	return base
}

// SaveGlobal writes the global config file, stripping the repo-specific fields
// (Repo, BaseBranch, Reviewers) so they are not stored at the global level.
func SaveGlobal(globalDir string, cfg Config) error {
	if err := os.MkdirAll(globalDir, 0755); err != nil {
		return fmt.Errorf("creating global config dir: %w", err)
	}

	// Zero out repo-specific fields before persisting.
	cfg.Repo = ""
	cfg.BaseBranch = ""
	cfg.Reviewers = nil

	data, err := marshalGlobalWithComments(cfg)
	if err != nil {
		return fmt.Errorf("marshaling global config: %w", err)
	}

	p := GlobalPath(globalDir)
	header := "# ragrev global configuration — edit with: ragrev config --global\n" +
		"# Values here seed new per-repo configs and supply defaults at runtime.\n" +
		"# repo, base_branch, and reviewers are omitted (they are per-repo only).\n\n"

	if err := os.WriteFile(p, []byte(header+string(data)), 0644); err != nil {
		return fmt.Errorf("writing global config: %w", err)
	}
	return nil
}

// EnsureGlobalExists creates the global config file with defaults if it does
// not yet exist (lazy creation).  Returns the config and its path.
func EnsureGlobalExists(globalDir string) (Config, string, error) {
	p := GlobalPath(globalDir)
	if _, err := os.Stat(p); err == nil {
		cfg, err := Load(globalDir)
		return cfg, p, err
	}
	cfg := Default()
	if err := SaveGlobal(globalDir, cfg); err != nil {
		return cfg, p, err
	}
	return cfg, p, nil
}

// LoadGlobalOrDefault loads the global config from globalDir if the file
// exists and returns it; otherwise it returns Default().  It never creates
// any files.
func LoadGlobalOrDefault(globalDir string) Config {
	if _, err := os.Stat(GlobalPath(globalDir)); err != nil {
		return Default()
	}
	cfg, err := Load(globalDir)
	if err != nil {
		return Default()
	}
	return cfg
}

// EnsureExistsSeeded is like EnsureExists but, when creating the config file
// for the first time, seeds its non-repo fields from the global config at
// globalDir (if that file already exists).  Pass an empty globalDir to skip
// seeding.
func EnsureExistsSeeded(ragrevDir, globalDir string) (Config, string, error) {
	p := Path(ragrevDir)
	if _, err := os.Stat(p); err == nil {
		cfg, err := Load(ragrevDir)
		return cfg, p, err
	}

	cfg := Default()
	if globalDir != "" {
		if _, serr := os.Stat(GlobalPath(globalDir)); serr == nil {
			if gCfg, lerr := Load(globalDir); lerr == nil {
				cfg = ApplyGlobal(cfg, gCfg)
			}
		}
	}

	if err := Save(ragrevDir, cfg); err != nil {
		return cfg, p, err
	}
	return cfg, p, nil
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
	// Ensure every built-in backend is present and back-fill missing fields.
	for name, defaultBackend := range defaults.Backends {
		if existing, ok := cfg.Backends[name]; !ok {
			cfg.Backends[name] = defaultBackend
		} else {
			cfg.Backends[name] = mergeBackendDefaults(existing, defaultBackend)
		}
	}
	// Aliased backends ("client(alias)") inherit their base client's defaults
	// (url, token settings, and model) for any field the user left blank.
	for name, existing := range cfg.Backends {
		client, alias := ParseBackendName(name)
		if alias == "" {
			continue
		}
		if def, ok := defaults.Backends[client]; ok {
			cfg.Backends[name] = mergeBackendDefaults(existing, def)
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
	return EnsureExistsSeeded(ragrevDir, "")
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
	return UpdateSeeded(ragrevDir, "")
}

// UpdateSeeded is like Update but seeds the config from the global config at
// globalDir when creating the file for the first time.  Pass an empty
// globalDir to skip seeding (equivalent to Update).
func UpdateSeeded(ragrevDir, globalDir string) (Config, []string, error) {
	p := Path(ragrevDir)

	data, readErr := os.ReadFile(p)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			// First run — seed from global (if present), then save.
			cfg, _, err := EnsureExistsSeeded(ragrevDir, globalDir)
			return cfg, nil, err
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

	// Warn about backends whose client prefix is not a recognised client type.
	knownClients := Default().Backends
	var badBackends []string
	for name := range cfg.Backends {
		client, _ := ParseBackendName(name)
		if _, ok := knownClients[client]; !ok {
			badBackends = append(badBackends, name)
		}
	}
	sort.Strings(badBackends)
	for _, name := range badBackends {
		client, _ := ParseBackendName(name)
		warnings = append(warnings, fmt.Sprintf(
			"backend %q uses unknown client %q; valid clients: %s",
			name, client, strings.Join(KnownClients(), ", ")))
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
	return launchEditor(p)
}

// EditGlobal opens the global config file in the user's default editor,
// creating it with defaults on first use.
func EditGlobal(globalDir string) error {
	_, p, err := EnsureGlobalExists(globalDir)
	if err != nil {
		return err
	}
	return launchEditor(p)
}

// launchEditor opens the file at path p in the user's preferred editor.
func launchEditor(p string) error {
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
