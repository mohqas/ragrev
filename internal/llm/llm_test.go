package llm

import (
	"bytes"
	"os"
	"testing"

	"github.com/mohqas/ragrev/internal/config"
)

func TestNewProvider_TokenEnv(t *testing.T) {
	os.Setenv("TEST_TOKEN_ENV", "env_secret")
	defer os.Unsetenv("TEST_TOKEN_ENV")

	cfg := config.BackendConfig{
		Model:    "test-model",
		URL:      "http://example.local",
		TokenEnv: "TEST_TOKEN_ENV",
		TokenCmd: "echo cmd_secret",
	}

	p, err := NewProvider("github", cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c, ok := p.(*openaiClient)
	if !ok {
		t.Fatalf("expected *openaiClient")
	}

	if c.token != "env_secret" {
		t.Errorf("token = %q, want env_secret", c.token)
	}
}

func TestNewProvider_TokenCmd(t *testing.T) {
	// unset to force fallback
	os.Unsetenv("TEST_TOKEN_ENV")

	cfg := config.BackendConfig{
		Model:    "test-model",
		URL:      "http://example.local",
		TokenEnv: "TEST_TOKEN_ENV",
		TokenCmd: "echo cmd_secret",
	}

	p, err := NewProvider("github", cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c, ok := p.(*openaiClient)
	if !ok {
		t.Fatalf("expected *openaiClient")
	}

	if c.token != "cmd_secret" {
		t.Errorf("token = %q, want cmd_secret", c.token)
	}
}

func TestNewProvider_OllamaNoAuth(t *testing.T) {
	cfg := config.BackendConfig{
		Model: "llama",
		URL:   "http://localhost",
	}

	p, err := NewProvider("ollama", cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	c, ok := p.(*openaiClient)
	if !ok {
		t.Fatalf("expected *openaiClient")
	}

	if c.token != "" {
		t.Errorf("token = %q, want empty", c.token)
	}
}

