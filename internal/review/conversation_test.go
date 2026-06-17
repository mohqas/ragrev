package review

import (
	"bytes"
	"testing"

	"github.com/canonical/ragrev/internal/llm"
	"github.com/canonical/ragrev/internal/logger"
)

func quietLogger() *logger.Logger {
	return logger.New(&bytes.Buffer{}, false, false)
}

func TestValidateScope(t *testing.T) {
	cases := map[string]struct {
		in      string
		want    ScopeMode
		wantErr bool
	}{
		"empty defaults to pr": {in: "", want: ScopePR},
		"pr":                   {in: "pr", want: ScopePR},
		"file":                 {in: "file", want: ScopeFile},
		"chunk":                {in: "chunk", want: ScopeChunk},
		"mixed case":           {in: "  Chunk ", want: ScopeChunk},
		"invalid":              {in: "global", wantErr: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := validateScope(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tc.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func bigContent(approxTokens int) string {
	// EstimateTokens uses ~4 chars/token; produce 4x bytes.
	b := make([]byte, approxTokens*4)
	for i := range b {
		b[i] = 'a'
	}
	return string(b)
}

func TestConversation_PruneDropsOldestChunkTurns(t *testing.T) {
	log := quietLogger()
	conv := newConversation(llm.Message{Role: "system", Content: "sys"}, 1000, log)

	// Add 3 chunk user/assistant pairs, ~300 tokens each.
	for i := 0; i < 3; i++ {
		conv.append(convEntry{
			msg:  llm.Message{Role: "user", Content: bigContent(300)},
			kind: kindChunkUser, file: "a.go",
		})
		conv.append(convEntry{
			msg:  llm.Message{Role: "assistant", Content: bigContent(10)},
			kind: kindChunkAssistant, file: "a.go",
		})
	}
	before := conv.estTokens()
	if before < 900 {
		t.Fatalf("setup did not produce a large enough conversation: %d", before)
	}

	conv.prune(map[string]bool{}, 400)
	after := conv.estTokens()
	if after >= before {
		t.Fatalf("prune did not shrink conversation: before=%d after=%d", before, after)
	}
	// System message must still be present.
	if conv.entries[0].kind != kindSystem {
		t.Fatalf("system message lost")
	}
}

func TestConversation_PruneEvictsCompletedPreloadFirst(t *testing.T) {
	log := quietLogger()
	conv := newConversation(llm.Message{Role: "system", Content: "sys"}, 1000, log)

	// Two preloaded files, one completed.
	conv.append(convEntry{
		msg:  llm.Message{Role: "user", Content: bigContent(500)},
		kind: kindPreloadFile, file: "done.go",
	})
	conv.append(convEntry{
		msg:  llm.Message{Role: "user", Content: bigContent(500)},
		kind: kindPreloadFile, file: "pending.go",
	})

	conv.prune(map[string]bool{"done.go": true}, 700)

	// done.go must have been evicted, pending.go must still hold its content.
	var doneEvicted, pendingEvicted bool
	for _, e := range conv.entries {
		if e.file == "done.go" {
			doneEvicted = e.evicted
		}
		if e.file == "pending.go" {
			pendingEvicted = e.evicted
		}
	}
	if !doneEvicted {
		t.Fatalf("expected done.go to be evicted")
	}
	if pendingEvicted {
		t.Fatalf("did not expect pending.go to be evicted yet")
	}
}

func TestConversation_HasFile(t *testing.T) {
	log := quietLogger()
	conv := newConversation(llm.Message{Role: "system", Content: "sys"}, 1000, log)
	conv.append(convEntry{
		msg: llm.Message{Role: "user", Content: "x"}, kind: kindPreloadFile, file: "a.go",
	})
	if !conv.hasFile("a.go") {
		t.Fatalf("hasFile should be true")
	}
	// Evict it and verify hasFile flips.
	conv.evictAt(1)
	if conv.hasFile("a.go") {
		t.Fatalf("hasFile should be false after eviction")
	}
}

