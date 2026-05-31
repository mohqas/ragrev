package config

import (
	"os"
	"path/filepath"
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
	if time.Duration(cfg.RateDelay) != 1*time.Second {
		t.Errorf("RateDelay = %v, want 1s", time.Duration(cfg.RateDelay))
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
	if time.Duration(loaded.RateDelay) != 1*time.Second {
		t.Errorf("RateDelay = %v, want 1s", time.Duration(loaded.RateDelay))
	}
	if loaded.Backend != "gemini" {
		t.Errorf("Backend = %q, want gemini", loaded.Backend)
	}
	if loaded.Backends["gemini"].Model != "gemini-2" {
		t.Errorf("Backend Model = %q, want gemini-2", loaded.Backends["gemini"].Model)
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
