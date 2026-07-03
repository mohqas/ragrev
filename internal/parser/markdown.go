package parser

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

type markdownParser struct{}

func init() {
	Register(".md", &markdownParser{})
	Register(".markdown", &markdownParser{})
}

func (p *markdownParser) Parse(patch string) ([]Chunk, error) {
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

func (p *markdownParser) ParseFull(fullSource string, patch string) ([]Chunk, error) {
	nodes := p.getNodes(fullSource)
	return BuildChunksFromFullFile(patch, nodes)
}

type headingData struct {
	name     string
	startRow int
}

func (p *markdownParser) getNodes(src string) []NodeCoord {
	source := []byte(src)
	doc := goldmark.New().Parser().Parse(text.NewReader(source))

	var headings []headingData

	_ = ast.Walk(doc, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && node.Kind() == ast.KindHeading {
			h := node.(*ast.Heading)
			if h.Lines().Len() > 0 {
				firstSeg := h.Lines().At(0)
				startRow := byteOffsetToRow(source, firstSeg.Start)

				var sb strings.Builder
				for i := 0; i < h.Lines().Len(); i++ {
					seg := h.Lines().At(i)
					sb.Write(source[seg.Start:seg.Stop])
				}
				name := strings.TrimSpace(sb.String())
				if name == "" {
					name = "Section"
				}

				headings = append(headings, headingData{
					name:     name,
					startRow: startRow,
				})
			}
		}
		return ast.WalkContinue, nil
	})

	if len(headings) == 0 {
		return nil
	}

	// Splitting with strings.Split creates totalLines elements.
	// We must handle empty files correctly.
	lines := strings.Split(src, "\n")
	totalLines := len(lines)
	if totalLines > 0 && src != "" && src[len(src)-1] == '\n' {
		// If it ends with a newline, strings.Split has an extra empty string at the end.
		totalLines--
	}

	var nodes []NodeCoord

	// If there's content before the first heading, add a preamble
	if headings[0].startRow > 0 {
		hasContent := false
		for i := 0; i < headings[0].startRow && i < len(lines); i++ {
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

	// Create flat contiguous sections
	for i := 0; i < len(headings); i++ {
		start := headings[i].startRow
		end := totalLines - 1
		if i+1 < len(headings) {
			end = headings[i+1].startRow - 1
		}
		if end < start {
			end = start
		}
		nodes = append(nodes, NodeCoord{
			Name:     headings[i].name,
			Kind:     "section",
			StartRow: start,
			EndRow:   end,
		})
	}

	return nodes
}

func byteOffsetToRow(source []byte, offset int) int {
	if offset < 0 {
		return 0
	}
	if offset > len(source) {
		offset = len(source)
	}
	row := 0
	for i := 0; i < offset && i < len(source); i++ {
		if source[i] == '\n' {
			row++
		}
	}
	return row
}
