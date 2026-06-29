package review

import (
	"fmt"

	"github.com/canonical/ragrev/internal/llm"
	"github.com/canonical/ragrev/internal/logger"
)

// usageStats aggregates token consumption across all LLM calls during a review.
type usageStats struct {
	model            string
	calls            int // successful completions
	retries          int // extra calls triggered by token-limit recovery
	skipped          int // chunks skipped after exhausting recovery
	promptTokens     int
	completionTokens int
	totalTokens      int
	estimated        bool // true if any call's numbers were estimated
}

// add records a single completion's usage.
func (s *usageStats) add(u llm.Usage) {
	s.calls++
	s.promptTokens += u.PromptTokens
	s.completionTokens += u.CompletionTokens
	s.totalTokens += u.TotalTokens
	if u.Estimated {
		s.estimated = true
	}
}

// report writes a concise token-usage summary to the logger. A verbose
// breakdown is shown when verbose logging is enabled.
func (s *usageStats) report(log *logger.Logger) {
	if s.calls == 0 {
		return
	}
	note := ""
	if s.estimated {
		note = " (estimated)"
	}
	log.Print("\n--- Token Usage ---\n")
	log.Print("Model: %s | calls: %d", s.model, s.calls)
	if s.retries > 0 {
		log.Print(" | retries: %d", s.retries)
	}
	if s.skipped > 0 {
		log.Print(" | skipped: %d", s.skipped)
	}
	log.Print("\n")
	log.Print("Tokens%s — prompt: %s, output: %s, total: %s\n",
		note, fmtInt(s.promptTokens), fmtInt(s.completionTokens), fmtInt(s.totalTokens))
	if s.calls > 0 {
		log.Verbose("Average per call — prompt: %d, output: %d, total: %d\n",
			s.promptTokens/s.calls, s.completionTokens/s.calls, s.totalTokens/s.calls)
	}
}

// fmtInt formats an integer with thousands separators.
func fmtInt(n int) string {
	s := fmt.Sprintf("%d", n)
	if n < 1000 {
		return s
	}
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}
