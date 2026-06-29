package review

import (
	"bytes"
	"strings"
	"testing"

	"github.com/canonical/ragrev/internal/llm"
	"github.com/canonical/ragrev/internal/logger"
)

func TestUsageStats_Add(t *testing.T) {
	var s usageStats
	s.add(llm.Usage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120})
	s.add(llm.Usage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60, Estimated: true})

	if s.calls != 2 {
		t.Errorf("calls = %d, want 2", s.calls)
	}
	if s.promptTokens != 150 || s.completionTokens != 30 || s.totalTokens != 180 {
		t.Errorf("tokens = %d/%d/%d, want 150/30/180", s.promptTokens, s.completionTokens, s.totalTokens)
	}
	if !s.estimated {
		t.Errorf("expected estimated to be true")
	}
}

func TestUsageStats_ReportEmpty(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, false, false)
	(&usageStats{}).report(log)
	if buf.Len() != 0 {
		t.Errorf("expected no output for zero calls, got %q", buf.String())
	}
}

func TestUsageStats_ReportContents(t *testing.T) {
	var buf bytes.Buffer
	log := logger.New(&buf, false, false)
	s := usageStats{model: "gpt-4o", retries: 1, skipped: 2}
	s.add(llm.Usage{PromptTokens: 1234, CompletionTokens: 56, TotalTokens: 1290})
	s.report(log)
	out := buf.String()
	for _, want := range []string{"Token Usage", "gpt-4o", "retries: 1", "skipped: 2", "1,234", "1,290"} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q in:\n%s", want, out)
		}
	}
}

func TestFmtInt(t *testing.T) {
	cases := map[int]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"}
	for in, want := range cases {
		if got := fmtInt(in); got != want {
			t.Errorf("fmtInt(%d) = %q, want %q", in, got, want)
		}
	}
}

