// Package output provides rich, GitHub PR–style terminal rendering for ragrev
// review results. When stdout is not a TTY (or --plain is requested) every
// method falls back to minimal plain text so the output remains pipe-friendly.
package output

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// termWidth returns the current terminal width, falling back to 100.
func termWidth() int {
	if w, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && w > 0 {
		return w
	}
	return 100
}

// Printer writes review output to an io.Writer with optional rich formatting.
type Printer struct {
	w     io.Writer
	plain bool
	theme string

	// lipgloss styles — populated only when plain=false
	fileHeaderStyle  lipgloss.Style
	chunkHeaderStyle lipgloss.Style
	snippetStyle     lipgloss.Style
	commentStyle     lipgloss.Style
	progressStyle    lipgloss.Style
	lgtmStyle        lipgloss.Style
	dividerStyle     lipgloss.Style
}

// New creates a Printer. When plain is false and stdout is not a TTY the
// printer automatically falls back to plain mode.
func New(w io.Writer, theme string, plain bool) *Printer {
	if !plain {
		// Auto-detect TTY — disable rich output when piped.
		if f, ok := w.(*os.File); ok {
			if !term.IsTerminal(int(f.Fd())) {
				plain = true
			}
		}
	}

	if theme == "" {
		theme = "github"
	}

	p := &Printer{w: w, plain: plain, theme: theme}
	if !plain {
		p.initStyles()
	}
	return p
}

// Plain reports whether the printer is operating in plain (no-colour) mode.
func (p *Printer) Plain() bool { return p.plain }

func (p *Printer) initStyles() {
	// ── File header ────────────────────────────────────────────────
	// Bold white text on a GitHub-blue background, full width.
	p.fileHeaderStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("#ffffff")).
		Background(lipgloss.Color("#0366d6")).
		Padding(0, 1)

	// ── Chunk sub-header ───────────────────────────────────────────
	p.chunkHeaderStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#586069")).
		Bold(true).
		PaddingLeft(2)

	// ── Snippet block ──────────────────────────────────────────────
	// Thin left border, subtle background.
	p.snippetStyle = lipgloss.NewStyle().
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("#0366d6")).
		PaddingLeft(1).
		Foreground(lipgloss.Color("#24292e"))

	// ── Comment body ───────────────────────────────────────────────
	// Amber/yellow left border to mimic a GH inline suggestion block.
	p.commentStyle = lipgloss.NewStyle().
		BorderLeft(true).
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("#f9c513")).
		PaddingLeft(1).
		PaddingTop(0).
		PaddingBottom(0)

	// ── Progress / status messages ─────────────────────────────────
	p.progressStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#586069")).
		Italic(true)

	// ── LGTM ───────────────────────────────────────────────────────
	p.lgtmStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#28a745")).
		Bold(true)

	// ── Divider ────────────────────────────────────────────────────
	p.dividerStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#e1e4e8"))
}

// PrintProgress writes a subtle italic status line (e.g. "⟳ Generating review for …").
func (p *Printer) PrintProgress(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if p.plain {
		fmt.Fprint(p.w, msg)
		return
	}
	fmt.Fprintln(p.w, p.progressStyle.Render("⟳  "+strings.TrimRight(msg, "\n")))
}

// PrintFileHeader renders a bold full-width banner for a file path.
func (p *Printer) PrintFileHeader(filename string) {
	if p.plain {
		fmt.Fprintf(p.w, "\n### %s\n", filename)
		return
	}
	width := termWidth()
	label := "  📄 " + filename
	// Pad to full width.
	padding := width - lipgloss.Width(label) - 2 // 2 for side padding
	if padding < 0 {
		padding = 0
	}
	rendered := p.fileHeaderStyle.Width(width - 2).Render(label)
	_ = padding
	fmt.Fprintln(p.w)
	fmt.Fprintln(p.w, rendered)
}

// PrintChunkHeader renders an indented sub-header for a chunk name + kind.
func (p *Printer) PrintChunkHeader(name, kind string) {
	if p.plain {
		fmt.Fprintf(p.w, "\n#### %s\n", name)
		return
	}
	width := termWidth()
	divider := p.dividerStyle.Render(strings.Repeat("─", width-4))
	header := p.chunkHeaderStyle.Render(fmt.Sprintf("  ƒ  %s  (%s)", name, kind))
	fmt.Fprintln(p.w, divider)
	fmt.Fprintln(p.w, header)
}

// PrintComment renders optional snippet diff lines followed by the review
// comment body. snippet may be empty (when the LLM commented on the whole
// chunk rather than specific lines).
func (p *Printer) PrintComment(snippet, body string) {
	snippet = strings.TrimSpace(snippet)
	body = strings.TrimSpace(body)

	if p.plain {
		if snippet != "" {
			fmt.Fprintf(p.w, "\n```diff\n%s\n```\n", snippet)
		}
		fmt.Fprintf(p.w, "\n%s\n", body)
		return
	}

	if snippet != "" {
		highlighted := p.highlightDiff(snippet)
		fmt.Fprintln(p.w, p.snippetStyle.Render(highlighted))
	}

	// Render the review comment with an amber left border.
	fmt.Fprintln(p.w, p.commentStyle.Width(termWidth()-6).Render(body))
	fmt.Fprintln(p.w)
}

// PrintLGTM writes a subtle LGTM indicator for a chunk that had no issues.
func (p *Printer) PrintLGTM() {
	if p.plain {
		return // stay silent in plain mode; nothing to say
	}
	fmt.Fprintln(p.w, p.lgtmStyle.Render("  ✔  LGTM"))
}

// PrintSummary writes a final summary line (e.g. token usage).
func (p *Printer) PrintSummary(msg string) {
	if p.plain {
		fmt.Fprint(p.w, msg)
		return
	}
	fmt.Fprintln(p.w, p.progressStyle.Render(strings.TrimRight(msg, "\n")))
}

// PrintNoIssues writes a top-level "no issues" message.
func (p *Printer) PrintNoIssues() {
	if p.plain {
		fmt.Fprintln(p.w, "No issues found to report.")
		return
	}
	fmt.Fprintln(p.w, p.lgtmStyle.Render("\n✔  No issues found to report."))
}

// highlightDiff uses chroma to syntax-colour a unified diff snippet.
func (p *Printer) highlightDiff(patch string) string {
	lexer := lexers.Get("diff")
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	style := styles.Get(p.theme)
	if style == nil {
		style = styles.Fallback
	}

	formatter := formatters.Get("terminal256")
	if formatter == nil {
		formatter = formatters.Fallback
	}

	iterator, err := lexer.Tokenise(nil, patch)
	if err != nil {
		return patch
	}

	var sb strings.Builder
	if err := formatter.Format(&sb, style, iterator); err != nil {
		return patch
	}
	return strings.TrimRight(sb.String(), "\n")
}
