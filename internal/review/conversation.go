package review

import (
	"fmt"
	"strings"

	"github.com/canonical/ragrev/internal/llm"
	"github.com/canonical/ragrev/internal/logger"
)

// ScopeMode controls how much PR context is loaded into the LLM conversation.
type ScopeMode string

const (
	// ScopePR preloads the full content of every changed file before reviewing chunks.
	ScopePR ScopeMode = "pr"
	// ScopeFile injects each file's full content lazily, the first time one of its
	// chunks is reviewed. Prior files remain in the conversation history.
	ScopeFile ScopeMode = "file"
	// ScopeChunk sends only the relevant file content + the chunk per call,
	// discarding history between chunks.
	ScopeChunk ScopeMode = "chunk"
)

// validateScope normalises and validates a scope value.
func validateScope(s string) (ScopeMode, error) {
	switch ScopeMode(strings.ToLower(strings.TrimSpace(s))) {
	case "", ScopePR:
		return ScopePR, nil
	case ScopeFile:
		return ScopeFile, nil
	case ScopeChunk:
		return ScopeChunk, nil
	default:
		return "", fmt.Errorf("invalid review scope %q (want one of: pr, file, chunk)", s)
	}
}

// msgKind tags each conversation entry so the pruner can make informed
// eviction decisions.
type msgKind int

const (
	kindSystem      msgKind = iota
	kindPreloadFile         // user msg holding a file's full content (PR-wide preload)
	kindPreloadAck          // single assistant ack right after the preload block
	kindFileHeader          // user msg announcing a file (scope=file mode)
	kindFileHeaderAck
	kindChunkUser
	kindChunkAssistant
)

// convEntry pairs a message with metadata used by the pruner.
type convEntry struct {
	msg  llm.Message
	kind msgKind
	// file is the path the entry relates to ("" for system / non-file entries).
	file string
	// evicted is true when the original content has been replaced by a stub.
	evicted bool
}

// conversation manages the rolling LLM context with awareness of preloaded
// file blocks, file headers and per-chunk turns. It supports proactive sizing
// and reactive pruning on token-limit errors.
type conversation struct {
	entries []convEntry
	budget  int // max prompt tokens; 0 disables budgeting
	log     *logger.Logger
}

func newConversation(sys llm.Message, budget int, log *logger.Logger) *conversation {
	return &conversation{
		entries: []convEntry{{msg: sys, kind: kindSystem}},
		budget:  budget,
		log:     log,
	}
}

// messages returns the flat slice ready to send to the LLM.
func (c *conversation) messages() []llm.Message {
	out := make([]llm.Message, 0, len(c.entries))
	for _, e := range c.entries {
		out = append(out, e.msg)
	}
	return out
}

// estTokens returns the current estimated prompt size.
func (c *conversation) estTokens() int {
	return llm.EstimateMessagesTokens(c.messages())
}

// append pushes an entry to the conversation tail.
func (c *conversation) append(e convEntry) {
	c.entries = append(c.entries, e)
}

// dropLastN removes the trailing N entries (used to roll back unsent turns).
func (c *conversation) dropLastN(n int) {
	if n <= 0 || n > len(c.entries) {
		return
	}
	c.entries = c.entries[:len(c.entries)-n]
}

// hasFile reports whether a file's full content is currently present in the
// conversation (not evicted).
func (c *conversation) hasFile(path string) bool {
	for _, e := range c.entries {
		if e.file == path && !e.evicted &&
			(e.kind == kindPreloadFile || e.kind == kindFileHeader) {
			return true
		}
	}
	return false
}

// prune drops content from the conversation until the estimated size fits
// under target. It preserves the system message and (when possible) the
// preloaded file context for files still pending review.
//
// Eviction order:
//  1. Oldest (chunkUser, chunkAssistant) pairs from the tail.
//  2. Preload entries for files in completed{}, replaced by short stubs.
//  3. Remaining preload entries oldest first, replaced by stubs.
//  4. Anything still left in the tail that's not the most recent turn.
func (c *conversation) prune(completed map[string]bool, target int) {
	if c.budget == 0 || target <= 0 {
		return
	}

	// Step 1: drop oldest chunk pairs.
	for c.estTokens() > target {
		i := c.firstChunkPairIndex()
		if i < 0 {
			break
		}
		// Drop the chunkUser + (optional) chunkAssistant immediately after.
		end := i + 1
		if end < len(c.entries) && c.entries[end].kind == kindChunkAssistant {
			end++
		}
		c.entries = append(c.entries[:i], c.entries[end:]...)
		c.log.Verbose("  -> Pruned oldest chunk turn from conversation.\n")
	}

	// Step 2: evict preload for completed files.
	if c.estTokens() > target {
		for i := range c.entries {
			if c.estTokens() <= target {
				break
			}
			e := &c.entries[i]
			if e.evicted || e.file == "" {
				continue
			}
			if e.kind != kindPreloadFile && e.kind != kindFileHeader {
				continue
			}
			if completed[e.file] {
				c.evictAt(i)
			}
		}
	}

	// Step 3: evict any remaining preload entries (oldest first).
	if c.estTokens() > target {
		for i := range c.entries {
			if c.estTokens() <= target {
				break
			}
			e := &c.entries[i]
			if e.evicted || e.file == "" {
				continue
			}
			if e.kind != kindPreloadFile && e.kind != kindFileHeader {
				continue
			}
			c.evictAt(i)
		}
	}
}

// evictAt replaces an entry's content with a short stub.
func (c *conversation) evictAt(i int) {
	e := &c.entries[i]
	e.msg.Content = fmt.Sprintf("[FILE: %s — content omitted to free context space]", e.file)
	e.evicted = true
	c.log.Verbose("  -> Evicted preloaded content for %s to free context space.\n", e.file)
}

// firstChunkPairIndex returns the index of the oldest chunkUser entry, or -1.
func (c *conversation) firstChunkPairIndex() int {
	for i, e := range c.entries {
		if e.kind == kindChunkUser {
			return i
		}
	}
	return -1
}
