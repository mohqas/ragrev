package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefault(t *testing.T) {
	cfg := Default()
	if cfg.BaseBranch != "master" {
		t.Errorf("BaseBranch = %q, want master", cfg.BaseBranch)
	}
	if cfg.MinCommentLen != 10 {
		t.Errorf("MinCommentLen = %d, want 10", cfg.MinCommentLen)
	}
	if time.Duration(cfg.RateDelay) != 100*time.Millisecond {
		t.Errorf("RateDelay = %v, want 200ms", time.Duration(cfg.RateDelay))
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()

	cfg := Default()
	cfg.Repo = "owner/repo"
	cfg.Authors = []string{"alice", "bob"}
	cfg.BaseBranch = "main"
	cfg.MinCommentLen = 15
	cfg.Backend = "gemini"
	cfg.ReviewPrompt = "Only review for security issues."
	cfg.Backends = map[string]BackendConfig{
		"gemini": {
			Model:    "gemini-2",
			URL:      "http://example.com/gemini",
			TokenEnv: "MY_GEMINI_TOKEN",
			TokenCmd: "echo test",
		},
	}

	if err := Save(dir, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists
	p := Path(dir)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Verify contents
	data, _ := os.ReadFile(p)
	t.Logf("Config file:\n%s", data)

	loaded, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if loaded.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", loaded.Repo)
	}
	if loaded.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want main", loaded.BaseBranch)
	}
	if len(loaded.Authors) != 2 || loaded.Authors[0] != "alice" {
		t.Errorf("Authors = %v, want [alice bob]", loaded.Authors)
	}
	if loaded.MinCommentLen != 15 {
		t.Errorf("MinCommentLen = %d, want 15", loaded.MinCommentLen)
	}
	if time.Duration(loaded.RateDelay) != 100*time.Millisecond {
		t.Errorf("RateDelay = %v, want 200ms", time.Duration(loaded.RateDelay))
	}
	if loaded.Backend != "gemini" {
		t.Errorf("Backend = %q, want gemini", loaded.Backend)
	}
	if loaded.Backends["gemini"].Model != "gemini-2" {
		t.Errorf("Backend Model = %q, want gemini-2", loaded.Backends["gemini"].Model)
	}
	if loaded.ReviewPrompt != "Only review for security issues." {
		t.Errorf("ReviewPrompt = %q, want %q", loaded.ReviewPrompt, "Only review for security issues.")
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()

	cfg, err := Load(dir)
	if err != nil {
		t.Fatalf("Load missing file should return defaults, got error: %v", err)
	}
	if cfg.BaseBranch != "master" {
		t.Errorf("missing file should return default BaseBranch=master, got %q", cfg.BaseBranch)
	}
}

func TestEnsureExists(t *testing.T) {
	dir := t.TempDir()

	cfg, p, err := EnsureExists(dir)
	if err != nil {
		t.Fatalf("EnsureExists: %v", err)
	}
	if cfg.BaseBranch != "master" {
		t.Errorf("BaseBranch = %q, want master", cfg.BaseBranch)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("config file not created at %s", p)
	}

	// Call again — should load existing
	cfg2, _, err := EnsureExists(dir)
	if err != nil {
		t.Fatalf("EnsureExists second call: %v", err)
	}
	if cfg2.BaseBranch != cfg.BaseBranch {
		t.Errorf("second call returned different config")
	}
}

func TestUpdateNewFile(t *testing.T) {
	dir := t.TempDir()

	cfg, warnings, err := Update(dir)
	if err != nil {
		t.Fatalf("Update on missing file: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings for new file, got %v", warnings)
	}
	if cfg.BaseBranch != "master" {
		t.Errorf("BaseBranch = %q, want master", cfg.BaseBranch)
	}
	if _, err := os.Stat(Path(dir)); err != nil {
		t.Errorf("config file not created: %v", err)
	}
}

func TestUpdateSeededFirstRun(t *testing.T) {
	globalDir := t.TempDir()
	repoDir := t.TempDir()

	// Set up global config with a custom theme.
	gCfg := Default()
	gCfg.OutputTheme = "monokai"
	gCfg.Backend = "ollama"
	if err := SaveGlobal(globalDir, gCfg); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	// First call: repo config does not exist — UpdateSeeded should seed from global.
	cfg, warnings, err := UpdateSeeded(repoDir, globalDir)
	if err != nil {
		t.Fatalf("UpdateSeeded (first run): %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings on first run: %v", warnings)
	}
	if cfg.OutputTheme != "monokai" {
		t.Errorf("OutputTheme = %q, want monokai (seeded from global)", cfg.OutputTheme)
	}
	if cfg.Backend != "ollama" {
		t.Errorf("Backend = %q, want ollama (seeded from global)", cfg.Backend)
	}

	// Confirm the seeded values were written to disk.
	reloaded, err := Load(repoDir)
	if err != nil {
		t.Fatalf("Load after UpdateSeeded: %v", err)
	}
	if reloaded.OutputTheme != "monokai" {
		t.Errorf("OutputTheme not persisted: got %q", reloaded.OutputTheme)
	}
}

func TestUpdateSeededExistingFileNotReseeded(t *testing.T) {
	globalDir := t.TempDir()
	repoDir := t.TempDir()

	// Write global config.
	gCfg := Default()
	gCfg.Backend = "ollama"
	if err := SaveGlobal(globalDir, gCfg); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	// Create the repo config first with a different backend.
	repoCfg := Default()
	repoCfg.Backend = "gemini"
	if err := Save(repoDir, repoCfg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// UpdateSeeded should NOT overwrite the existing per-repo value.
	cfg, _, err := UpdateSeeded(repoDir, globalDir)
	if err != nil {
		t.Fatalf("UpdateSeeded (existing file): %v", err)
	}
	if cfg.Backend != "gemini" {
		t.Errorf("Backend = %q, existing per-repo value should win over global", cfg.Backend)
	}
}

func TestUpdateMergesNewDefaults(t *testing.T) {
	dir := t.TempDir()

	// Write a minimal config that is missing several default fields.
	minimal := "repo: owner/repo\nbase_branch: main\n"
	if err := os.WriteFile(Path(dir), []byte(minimal), 0644); err != nil {
		t.Fatalf("writing minimal config: %v", err)
	}

	cfg, warnings, err := Update(dir)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("expected no warnings, got %v", warnings)
	}

	// New defaults should have been merged in.
	if cfg.ReviewScope == "" {
		t.Errorf("ReviewScope should be filled in from defaults")
	}
	if cfg.Backend == "" {
		t.Errorf("Backend should be filled in from defaults")
	}

	// Re-read the file to confirm defaults were written.
	reloaded, err := Load(dir)
	if err != nil {
		t.Fatalf("reloading: %v", err)
	}
	if reloaded.ReviewScope != cfg.ReviewScope {
		t.Errorf("ReviewScope not persisted after Update")
	}
}

func TestUpdateWarnsOnRemovedFields(t *testing.T) {
	dir := t.TempDir()

	// Write a config with a field that no longer exists.
	stale := "repo: owner/repo\nbase_branch: main\nold_removed_field: somevalue\n"
	if err := os.WriteFile(Path(dir), []byte(stale), 0644); err != nil {
		t.Fatalf("writing stale config: %v", err)
	}

	cfg, warnings, err := Update(dir)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(warnings) == 0 {
		t.Error("expected a warning about removed field, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "old_removed_field") {
			found = true
		}
	}
	if !found {
		t.Errorf("warning did not mention the removed field; warnings: %v", warnings)
	}

	// The config should still load cleanly.
	if cfg.Repo != "owner/repo" {
		t.Errorf("Repo = %q, want owner/repo", cfg.Repo)
	}
}

func TestUpdatePreservesUserValues(t *testing.T) {
	dir := t.TempDir()

	original := Default()
	original.Repo = "my-org/my-repo"
	original.BaseBranch = "develop"
	original.MinCommentLen = 42
	if err := Save(dir, original); err != nil {
		t.Fatalf("Save: %v", err)
	}

	cfg, warnings, err := Update(dir)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %v", warnings)
	}
	if cfg.Repo != "my-org/my-repo" {
		t.Errorf("Repo = %q, want my-org/my-repo", cfg.Repo)
	}
	if cfg.BaseBranch != "develop" {
		t.Errorf("BaseBranch = %q, want develop", cfg.BaseBranch)
	}
	if cfg.MinCommentLen != 42 {
		t.Errorf("MinCommentLen = %d, want 42", cfg.MinCommentLen)
	}
}

func TestPath(t *testing.T) {
	p := Path("/some/dir")
	want := filepath.Join("/some/dir", "config.yml")
	if p != want {
		t.Errorf("Path = %q, want %q", p, want)
	}
}

func TestGlobalPath(t *testing.T) {
	p := GlobalPath("/some/global")
	want := filepath.Join("/some/global", "config.yml")
	if p != want {
		t.Errorf("GlobalPath = %q, want %q", p, want)
	}
}

func TestSaveGlobalOmitsRepoFields(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	cfg.Repo = "owner/repo"
	cfg.BaseBranch = "main"
	cfg.Reviewers = []string{"alice"}
	cfg.Backend = "gemini"

	if err := SaveGlobal(dir, cfg); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	data, _ := os.ReadFile(GlobalPath(dir))
	content := string(data)
	if strings.Contains(content, "owner/repo") {
		t.Error("SaveGlobal should not persist Repo")
	}
	if strings.Contains(content, "alice") {
		t.Error("SaveGlobal should not persist Reviewers")
	}
	// Backend is a non-repo field and should be present
	if !strings.Contains(content, "gemini") {
		t.Error("SaveGlobal should persist Backend")
	}
}

func TestEnsureGlobalExists(t *testing.T) {
	dir := t.TempDir()

	cfg, p, err := EnsureGlobalExists(dir)
	if err != nil {
		t.Fatalf("EnsureGlobalExists: %v", err)
	}
	if cfg.Backend == "" {
		t.Errorf("expected non-empty Backend default")
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("global config file not created at %s", p)
	}

	// Second call should load existing file without re-creating.
	cfg2, _, err := EnsureGlobalExists(dir)
	if err != nil {
		t.Fatalf("EnsureGlobalExists second call: %v", err)
	}
	if cfg2.Backend != cfg.Backend {
		t.Errorf("second call returned different Backend")
	}
}

func TestApplyGlobal(t *testing.T) {
	base := Default()
	global := Default()
	global.Backend = "ollama"
	global.OutputTheme = "monokai"
	global.Repo = "should-not-appear"
	global.BaseBranch = "should-not-appear"
	global.Reviewers = []string{"should-not-appear"}

	result := ApplyGlobal(base, global)
	if result.Backend != "ollama" {
		t.Errorf("Backend = %q, want ollama", result.Backend)
	}
	if result.OutputTheme != "monokai" {
		t.Errorf("OutputTheme = %q, want monokai", result.OutputTheme)
	}
	if result.Repo != "" {
		t.Errorf("ApplyGlobal should not copy Repo, got %q", result.Repo)
	}
	if result.BaseBranch != "master" {
		t.Errorf("ApplyGlobal should not copy BaseBranch from global, got %q", result.BaseBranch)
	}
	if len(result.Reviewers) != 0 {
		t.Errorf("ApplyGlobal should not copy Reviewers, got %v", result.Reviewers)
	}
}

func TestEnsureExistsSeeded(t *testing.T) {
	globalDir := t.TempDir()
	repoDir := t.TempDir()

	// Set up a global config with a custom backend.
	gCfg := Default()
	gCfg.Backend = "ollama"
	if err := SaveGlobal(globalDir, gCfg); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	// First call: repo config does not exist yet — should be seeded from global.
	cfg, _, err := EnsureExistsSeeded(repoDir, globalDir)
	if err != nil {
		t.Fatalf("EnsureExistsSeeded: %v", err)
	}
	if cfg.Backend != "ollama" {
		t.Errorf("Backend = %q, want ollama (seeded from global)", cfg.Backend)
	}

	// Second call: repo config already exists — global should not override.
	cfg2, _, err := EnsureExistsSeeded(repoDir, globalDir)
	if err != nil {
		t.Fatalf("EnsureExistsSeeded second call: %v", err)
	}
	if cfg2.Backend != "ollama" {
		t.Errorf("Backend = %q on second call, want ollama", cfg2.Backend)
	}
}

func TestEnsureExistsSeededNoGlobal(t *testing.T) {
	repoDir := t.TempDir()
	globalDir := t.TempDir() // global dir exists but has no config.yml

	cfg, _, err := EnsureExistsSeeded(repoDir, globalDir)
	if err != nil {
		t.Fatalf("EnsureExistsSeeded without global file: %v", err)
	}
	// Should fall back to Default()
	if cfg.Backend != Default().Backend {
		t.Errorf("Backend = %q, want default %q", cfg.Backend, Default().Backend)
	}
}

func TestLoadGlobalOrDefault(t *testing.T) {
	dir := t.TempDir()

	// No file → returns Default()
	cfg := LoadGlobalOrDefault(dir)
	if cfg.Backend != Default().Backend {
		t.Errorf("LoadGlobalOrDefault (no file): Backend = %q, want default", cfg.Backend)
	}

	// Write a global config
	custom := Default()
	custom.Backend = "gemini"
	if err := SaveGlobal(dir, custom); err != nil {
		t.Fatalf("SaveGlobal: %v", err)
	}

	cfg = LoadGlobalOrDefault(dir)
	if cfg.Backend != "gemini" {
		t.Errorf("LoadGlobalOrDefault (file exists): Backend = %q, want gemini", cfg.Backend)
	}
}

func TestDurationYAML(t *testing.T) {
	cfg := Default()
	cfg.RateDelay = Duration(2500 * time.Millisecond)

	dir := t.TempDir()
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(loaded.RateDelay) != 2500*time.Millisecond {
		t.Errorf("RateDelay = %v, want 2.5s", time.Duration(loaded.RateDelay))
	}
}

func TestParseBackendName(t *testing.T) {
	cases := []struct {
		in         string
		wantClient string
		wantAlias  string
	}{
		{"github", "github", ""},
		{"github(opus)", "github", "opus"},
		{"gemini(fast)", "gemini", "fast"},
		{"(x)", "(x)", ""},       // no client prefix => treated as plain name
		{"weird(", "weird(", ""}, // unterminated => plain name
	}
	for _, c := range cases {
		client, alias := ParseBackendName(c.in)
		if client != c.wantClient || alias != c.wantAlias {
			t.Errorf("ParseBackendName(%q) = (%q, %q), want (%q, %q)",
				c.in, client, alias, c.wantClient, c.wantAlias)
		}
	}
}

func TestLoadAliasedBackendInheritsDefaults(t *testing.T) {
	dir := t.TempDir()
	cfg := Default()
	// Define an aliased github backend that only sets the model.
	cfg.Backends["github(opus)"] = BackendConfig{Model: "claude-opus-4-5"}
	cfg.Backend = "github(opus)"
	if err := Save(dir, cfg); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	bc, ok := loaded.Backends["github(opus)"]
	if !ok {
		t.Fatal("aliased backend github(opus) missing after load")
	}
	if bc.Model != "claude-opus-4-5" {
		t.Errorf("Model = %q, want claude-opus-4-5", bc.Model)
	}
	def := Default().Backends["github"]
	if bc.URL != def.URL {
		t.Errorf("URL = %q, want inherited %q", bc.URL, def.URL)
	}
	if bc.TokenEnv != def.TokenEnv {
		t.Errorf("TokenEnv = %q, want inherited %q", bc.TokenEnv, def.TokenEnv)
	}
}
