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
