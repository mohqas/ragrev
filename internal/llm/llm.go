// Package llm provides providers for sending completion requests to LLMs.
package llm

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mohqas/ragrev/internal/config"
	"github.com/mohqas/ragrev/internal/logger"
)

// Message is the universal minimum block needed to communicate with an LLM.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Provider represents an abstract LLM backend.
type Provider interface {
	Complete(ctx context.Context, messages []Message) (string, error)
}

// NewProvider returns a Provider implementation configured per backendCfg.
func NewProvider(name string, backendCfg config.BackendConfig, log *logger.Logger) (Provider, error) {
	var token string

	// Token resolution
	if backendCfg.TokenEnv != "" {
		token = os.Getenv(backendCfg.TokenEnv)
	}

	if token == "" && backendCfg.TokenCmd != "" {
		parts := strings.Split(backendCfg.TokenCmd, " ")
		cmd := exec.Command(parts[0], parts[1:]...)
		out, err := cmd.Output()
		if err == nil {
			token = strings.TrimSpace(string(out))
		}
	}

	switch name {
	case "github", "copilot":
		// GitHub Models and Copilot use standard OpenAI chat format.
		if token == "" {
			return nil, fmt.Errorf("token resolution failed: neither %s nor %q returned a token", backendCfg.TokenEnv, backendCfg.TokenCmd)
		}
		// GitHub expects "Bearer <token>" but OpenAI Client can just prepend Bearer if it's not empty
		return newOpenAIClient(backendCfg.URL, backendCfg.Model, token, log), nil

	case "ollama":
		// Ollama is an OpenAI-compatible API without auth.
		return newOpenAIClient(backendCfg.URL, backendCfg.Model, "", log), nil

	case "gemini":
		if token == "" {
			return nil, fmt.Errorf("token resolution failed: neither %s nor %q returned a token", backendCfg.TokenEnv, backendCfg.TokenCmd)
		}
		return newGeminiClient(backendCfg.URL, backendCfg.Model, token, log), nil

	default:
		return nil, fmt.Errorf("unknown LLM backend provider: %s", name)
	}
}
