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

// Usage reports the token cost of a single completion. PromptTokens and
// CompletionTokens are taken from the provider's response when available;
// otherwise they are coarse estimates. Estimated is true when any field
// was derived from EstimateTokens rather than reported by the API.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	Estimated        bool
}

// Provider represents an abstract LLM backend.
type Provider interface {
	Complete(ctx context.Context, messages []Message) (string, Usage, error)
	// MaxPromptTokens returns the maximum prompt size in tokens that ragrev
	// will pack into a single call to this provider.
	MaxPromptTokens() int
	// Model returns the configured model identifier.
	Model() string
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

	client, _ := config.ParseBackendName(name)
	switch client {
	case "github", "copilot":
		// GitHub Models and Copilot use standard OpenAI chat format.
		if token == "" {
			return nil, fmt.Errorf("token resolution failed: neither %s nor %q returned a token", backendCfg.TokenEnv, backendCfg.TokenCmd)
		}
		// GitHub expects "Bearer <token>" but OpenAI Client can just prepend Bearer if it's not empty
		return newOpenAIClient(backendCfg.URL, backendCfg.Model, token, resolveBudget(backendCfg), nil, log), nil

	case "ollama":
		// Ollama is an OpenAI-compatible API without auth.
		return newOpenAIClient(backendCfg.URL, backendCfg.Model, "", resolveBudget(backendCfg), nil, log), nil

	case "gemini":
		if token == "" {
			return nil, fmt.Errorf("token resolution failed: neither %s nor %q returned a token", backendCfg.TokenEnv, backendCfg.TokenCmd)
		}
		return newGeminiClient(backendCfg.URL, backendCfg.Model, token, resolveBudget(backendCfg), log), nil

	case "openrouter":
		// OpenRouter is an OpenAI-compatible gateway that routes to many
		// underlying models (Claude, Llama, GLM, Gemini, etc.) via a single
		// endpoint. HTTP-Referer and X-Title are courtesy attribution headers
		// recommended by OpenRouter.
		if token == "" {
			return nil, fmt.Errorf("token resolution failed: neither %s nor %q returned a token", backendCfg.TokenEnv, backendCfg.TokenCmd)
		}
		extraHeaders := map[string]string{
			"HTTP-Referer": "https://github.com/mohqas/ragrev",
			"X-Title":      "ragrev",
		}
		return newOpenAIClient(backendCfg.URL, backendCfg.Model, token, resolveBudget(backendCfg), extraHeaders, log), nil

	default:
		return nil, fmt.Errorf("unknown LLM backend client %q (backend %q); valid clients: %s",
			client, name, strings.Join(config.KnownClients(), ", "))
	}
}

// resolveBudget returns the configured MaxPromptTokens or a model-derived default.
func resolveBudget(cfg config.BackendConfig) int {
	if cfg.MaxPromptTokens > 0 {
		return cfg.MaxPromptTokens
	}
	return DefaultMaxPromptTokens(cfg.Model)
}
