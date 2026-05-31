package parser

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Chunk represents a split portion of a file diff.
type Chunk struct {
	Name    string
	Kind    string
	Patch   string
	EndLine int
}

// Parser defines the interface for language-specific diff splitters.
type Parser interface {
	Parse(patch string) ([]Chunk, error)
}

var registry = make(map[string]Parser)

// Register registers a parser for a specific file extension (e.g. ".go").
func Register(ext string, p Parser) {
	registry[ext] = p
}

// ForFile returns a parser for the given filename, or nil if none exists.
func ForFile(filename string) Parser {
	ext := strings.ToLower(filepath.Ext(filename))
	return registry[ext]
}

// ParsedDiff represents a reconstructed source and mapping back to diff lines.
type ParsedDiff struct {
	SourceLines  []string
	SourceToDiff []int
	DiffLines    []string
}

// ParsePatch reconstructs the new state of a file from a diff and builds maps.
func ParsePatch(patch string) *ParsedDiff {
	diffLines := strings.Split(patch, "\n")
	var sourceLines []string
	var srcToDiff []int

	for i, line := range diffLines {
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, "\\") {
			continue
		}
		content := line
		if len(line) > 0 && (line[0] == ' ' || line[0] == '+') {
			content = line[1:]
		}
		sourceLines = append(sourceLines, content)
		srcToDiff = append(srcToDiff, i)
	}
	return &ParsedDiff{
		SourceLines:  sourceLines,
		SourceToDiff: srcToDiff,
		DiffLines:    diffLines,
	}
}

// NodeCoord defines the line range (0-based) for a parsed AST node.
type NodeCoord struct {
	Name     string
	Kind     string
	StartRow int
	EndRow   int
}

// BuildChunks splits the original DiffLines based on node coordinates.
func BuildChunks(diff *ParsedDiff, nodes []NodeCoord) ([]Chunk, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("no nodes found")
	}

	assigned := make([]bool, len(diff.DiffLines))
	var chunks []Chunk

	for _, n := range nodes {
		if n.StartRow < 0 || n.EndRow >= len(diff.SourceLines) {
			continue
		}
		startIdx := diff.SourceToDiff[n.StartRow]
		endIdx := diff.SourceToDiff[n.EndRow]

		var chunkLines []string
		for j := startIdx; j <= endIdx; j++ {
			chunkLines = append(chunkLines, diff.DiffLines[j])
			assigned[j] = true
		}

		if len(chunkLines) > 0 {
			chunks = append(chunks, Chunk{
				Name:  n.Name,
				Kind:  n.Kind,
				Patch: strings.Join(chunkLines, "\n"),
			})
		}
	}

	// Create overhead chunk for unassigned lines that relate to additions
	var unassignedBlock []string
	hasAdded := false
	for j, a := range assigned {
		if !a {
			unassignedBlock = append(unassignedBlock, diff.DiffLines[j])
			if strings.HasPrefix(diff.DiffLines[j], "+") {
				hasAdded = true
			}
		}
	}

	if hasAdded && len(unassignedBlock) > 0 {
		chunks = append(chunks, Chunk{
			Name:  "overhead",
			Kind:  "overhead",
			Patch: strings.Join(unassignedBlock, "\n"),
		})
	}

	return chunks, nil
}

// FallbackChunk returns the whole diff as a single chunk.
func FallbackChunk(patch string) []Chunk {
	return []Chunk{
		{
			Name:  "file",
			Kind:  "file",
			Patch: patch,
		},
	}
}
