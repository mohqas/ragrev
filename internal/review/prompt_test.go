package review

import (
	"strings"
	"testing"
	"time"
)

func TestBuildSystemPrompt(t *testing.T) {
	now := time.Date(2026, time.June, 29, 0, 0, 0, 0, time.UTC)

	t.Run("default keeps built-in instructions", func(t *testing.T) {
		got := buildSystemPrompt("", now)
		if !strings.Contains(got, "June 29, 2026") {
			t.Errorf("missing date: %q", got)
		}
		if !strings.Contains(got, "senior code reviewer") {
			t.Errorf("default prompt should include the reviewer persona preamble")
		}
		if !strings.Contains(got, "LGTM_NO_ISSUES") {
			t.Errorf("default prompt should include LGTM instructions")
		}
		if !strings.Contains(got, "<snippet>") {
			t.Errorf("default prompt should include snippet instructions")
		}
	})

	t.Run("override keeps only the date and drops the persona/LGTM/snippet", func(t *testing.T) {
		got := buildSystemPrompt("Only review for security issues.", now)
		if !strings.HasPrefix(got, "The current date is June 29, 2026. ") {
			t.Errorf("override should start with the date sentence: %q", got)
		}
		if !strings.Contains(got, "Only review for security issues.") {
			t.Errorf("override prompt missing custom text: %q", got)
		}
		if strings.Contains(got, "senior code reviewer") {
			t.Errorf("override prompt should drop the reviewer persona preamble")
		}
		if strings.Contains(got, "LGTM_NO_ISSUES") {
			t.Errorf("override prompt should drop LGTM instructions")
		}
		if strings.Contains(got, "<snippet>") {
			t.Errorf("override prompt should drop snippet instructions")
		}
	})
}
