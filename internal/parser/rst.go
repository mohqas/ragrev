package parser

import (
	"strings"
	"unicode/utf8"
)

type rstParser struct{}

func init() {
	Register(".rst", &rstParser{})
	Register(".rest", &rstParser{})
}

func (p *rstParser) Parse(patch string) ([]Chunk, error) {
	diff := ParsePatch(patch)
	if len(diff.SourceLines) == 0 {
		return FallbackChunk(patch), nil
	}
	src := strings.Join(diff.SourceLines, "\n")
	nodes := p.getNodes(src)
	if len(nodes) == 0 {
		return FallbackChunk(patch), nil
	}
	chunks, err := BuildChunks(diff, nodes)
	if err != nil {
		return FallbackChunk(patch), nil
	}
	return chunks, nil
}

func (p *rstParser) ParseFull(fullSource string, patch string) ([]Chunk, error) {
	nodes := p.getNodes(fullSource)
	return BuildChunksFromFullFile(patch, nodes)
}

// rstHeading holds the discovered title text and the row where the section
// starts (which may be one row earlier than the title if an overline is present).
type rstHeading struct {
	name     string
	titleRow int // 0-based row of the title text line
	startRow int // 0-based row where the section visually begins (overline or title)
}

// rstPunct reports whether b is a printable ASCII punctuation character
// accepted by RST as an adornment character.
func rstPunct(b byte) bool {
	return (b >= '!' && b <= '/') ||
		(b >= ':' && b <= '@') ||
		(b >= '[' && b <= '`') ||
		(b >= '{' && b <= '~')
}

// isAdornmentLine returns true when every character in line is the same RST
// punctuation character and the line is at least one character long.
// Windows-style trailing \r is stripped before the check.
func isAdornmentLine(line string) bool {
	trimmed := strings.TrimRight(line, "\r")
	if len(trimmed) == 0 {
		return false
	}
	first := trimmed[0]
	if !rstPunct(first) {
		return false
	}
	for i := 1; i < len(trimmed); i++ {
		if trimmed[i] != first {
			return false
		}
	}
	return true
}

// adornmentLen returns the rune count of an adornment line (CR stripped).
func adornmentLen(line string) int {
	return utf8.RuneCountInString(strings.TrimRight(line, "\r"))
}

// getNodes scans source text and returns a flat []NodeCoord where each element
// corresponds to one RST section. Sections are contiguous: each section ends
// on the line immediately before the next section begins.
//
// A preamble node is added when there is non-blank content before the first
// heading (mirrors markdownParser behaviour).
func (p *rstParser) getNodes(src string) []NodeCoord {
	if src == "" {
		return nil
	}

	lines := strings.Split(src, "\n")
	totalLines := len(lines)
	// Trim the trailing empty element that strings.Split adds for a trailing newline.
	if totalLines > 0 && lines[totalLines-1] == "" {
		totalLines--
	}
	if totalLines == 0 {
		return nil
	}

	var headings []rstHeading

	// We need to skip lines that were already consumed as part of an
	// overline+title+underline triple so we don't re-detect the underline as
	// another heading's title.
	skip := make([]bool, totalLines)

	for i := 0; i < totalLines; i++ {
		if skip[i] {
			continue
		}

		// A section heading requires a non-empty, non-adornment title line
		// followed by an adornment line whose length >= the title's rune count.
		titleLine := strings.TrimRight(lines[i], "\r")
		titleText := strings.TrimSpace(titleLine)
		if titleText == "" || isAdornmentLine(titleLine) {
			continue
		}

		// Look ahead for underline.
		if i+1 >= totalLines {
			continue
		}
		underLine := lines[i+1]
		if !isAdornmentLine(underLine) {
			continue
		}
		if adornmentLen(underLine) < utf8.RuneCountInString(titleText) {
			continue
		}

		startRow := i
		// Check for a matching overline (same character and length as underline).
		if i > 0 && !skip[i-1] {
			overLine := strings.TrimRight(lines[i-1], "\r")
			underTrimmed := strings.TrimRight(underLine, "\r")
			if overLine == underTrimmed {
				startRow = i - 1
				skip[i-1] = true
			}
		}

		headings = append(headings, rstHeading{
			name:     titleText,
			titleRow: i,
			startRow: startRow,
		})

		// Mark the underline as consumed so it isn't treated as a title.
		skip[i+1] = true
		// Also mark the title itself — handled by the loop variable but belt-and-suspenders.
		skip[i] = true
		i++ // advance past the underline
	}

	if len(headings) == 0 {
		return nil
	}

	var nodes []NodeCoord

	// Preamble: non-blank content before the first heading.
	if headings[0].startRow > 0 {
		hasContent := false
		for i := 0; i < headings[0].startRow && i < totalLines; i++ {
			if strings.TrimSpace(lines[i]) != "" {
				hasContent = true
				break
			}
		}
		if hasContent {
			nodes = append(nodes, NodeCoord{
				Name:     "preamble",
				Kind:     "section",
				StartRow: 0,
				EndRow:   headings[0].startRow - 1,
			})
		}
	}

	// Build contiguous section nodes.
	for i, h := range headings {
		endRow := totalLines - 1
		if i+1 < len(headings) {
			endRow = headings[i+1].startRow - 1
		}
		if endRow < h.startRow {
			endRow = h.startRow
		}
		nodes = append(nodes, NodeCoord{
			Name:     h.name,
			Kind:     "section",
			StartRow: h.startRow,
			EndRow:   endRow,
		})
	}

	return nodes
}
