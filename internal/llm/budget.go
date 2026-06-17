package llm

import "strings"

// EstimateTokens returns a coarse upper-bound estimate of the number of
// tokens contained in s. It uses the well-known "≈4 chars per token"
// heuristic, which is sufficient for budgeting decisions without pulling
// in a model-specific tokenizer.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// Round up so short strings still cost at least 1 token.
	return (len(s) + 3) / 4
}

// EstimateMessagesTokens estimates the total tokens for a slice of messages,
// adding a small per-message overhead for role/structural framing.
func EstimateMessagesTokens(msgs []Message) int {
	total := 0
	for _, m := range msgs {
		total += EstimateTokens(m.Content) + 4 // role + framing
	}
	return total
}

// DefaultMaxPromptTokens returns ~75% of the advertised context window for
// well-known models. Returns a conservative fallback for unknown models so
// the caller always gets a positive budget to work with.
func DefaultMaxPromptTokens(model string) int {
	m := strings.ToLower(model)
	switch {
	// Gemini 1.5 / 2.x: ~1M context
	case strings.Contains(m, "gemini-1.5"),
		strings.Contains(m, "gemini-2"):
		return 750_000
	// Claude 3.5 / 4.x: 200k context
	case strings.Contains(m, "claude"):
		return 150_000
	// GPT-4o / 4-turbo: 128k context
	case strings.Contains(m, "gpt-4o"),
		strings.Contains(m, "gpt-4-turbo"),
		strings.Contains(m, "gpt-4.1"):
		return 96_000
	// GPT-4 base: 8k
	case strings.Contains(m, "gpt-4"):
		return 6_000
	// GPT-3.5-turbo-16k / general 3.5
	case strings.Contains(m, "gpt-3.5-turbo-16k"):
		return 12_000
	case strings.Contains(m, "gpt-3.5"):
		return 12_000
	// Llama 3 / 3.1 commonly 8k–128k depending on variant; pick the safe 8k figure.
	case strings.Contains(m, "llama-3.1"),
		strings.Contains(m, "llama3.1"),
		strings.Contains(m, "llama-3.2"),
		strings.Contains(m, "llama3.2"):
		return 96_000
	case strings.Contains(m, "llama3"),
		strings.Contains(m, "llama-3"),
		strings.Contains(m, "llama2"),
		strings.Contains(m, "llama-2"):
		return 6_000
	default:
		return 24_000
	}
}
