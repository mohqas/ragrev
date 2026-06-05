package parser

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+((\d+))(?:,\d+)? @@`)

// Chunk represents a split portion of a file diff.
type Chunk struct {
	Name        string
	Kind        string
	Patch       string
	StartLine   int
	EndLine     int
	TargetLines []int
}

// Parser defines the interface for language-specific diff splitters.
type Parser interface {
	Parse(patch string) ([]Chunk, error)
	ParseFull(fullSource string, patch string) ([]Chunk, error)
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
	SourceLines      []string
	SourceToDiff     []int
	DiffLines        []string
	DiffToTargetLine map[int]int
}

// ParsePatch reconstructs the new state of a file from a diff and builds maps.
func ParsePatch(patch string) *ParsedDiff {
	diffLines := strings.Split(patch, "\n")
	var sourceLines []string
	var srcToDiff []int
	diffToTargetLine := make(map[int]int)

	currentLine := 0
	for i, line := range diffLines {
		if strings.HasPrefix(line, "@@") {
			matches := hunkHeaderRe.FindStringSubmatch(line)
			if len(matches) > 1 {
				if parsed, err := strconv.Atoi(matches[1]); err == nil {
					currentLine = parsed
				}
			}
			continue
		}

		// Use the line after the prefix to determine currentLine mapping
		// unified diff: removals (-) relate to the current line number in the NEW file
		// where the new replacement (if any) or subsequent lines will be.

		diffToTargetLine[i] = currentLine

		if strings.HasPrefix(line, "-") || strings.HasPrefix(line, "\\") {
			continue
		}

		content := line
		if len(line) > 0 && (line[0] == ' ' || line[0] == '+') {
			content = line[1:]
		}
		sourceLines = append(sourceLines, content)
		srcToDiff = append(srcToDiff, i)

		currentLine++
	}
	return &ParsedDiff{
		SourceLines:      sourceLines,
		SourceToDiff:     srcToDiff,
		DiffLines:        diffLines,
		DiffToTargetLine: diffToTargetLine,
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
		var targetLines []int
		firstLine := 0
		lastLine := 0
		for j := startIdx; j <= endIdx; j++ {
			chunkLines = append(chunkLines, diff.DiffLines[j])
			assigned[j] = true
			if ln, ok := diff.DiffToTargetLine[j]; ok {
				if firstLine == 0 {
					firstLine = ln
				}
				lastLine = ln
				targetLines = append(targetLines, ln)
			} else {
				targetLines = append(targetLines, 0)
			}
		}

		if len(chunkLines) > 0 {
			chunks = append(chunks, Chunk{
				Name:        n.Name,
				Kind:        n.Kind,
				Patch:       strings.Join(chunkLines, "\n"),
				StartLine:   firstLine,
				EndLine:     lastLine,
				TargetLines: targetLines,
			})
		}
	}

	// Create overhead chunk for unassigned lines that relate to additions
	var unassignedBlock []string
	var unassignedTargetLines []int
	hasAdded := false
	firstUnassignedLine := 0
	lastUnassignedLine := 0
	for j, a := range assigned {
		line := diff.DiffLines[j]
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if !a {
			unassignedBlock = append(unassignedBlock, line)
			if strings.HasPrefix(line, "+") {
				hasAdded = true
			}
			if ln, ok := diff.DiffToTargetLine[j]; ok {
				if firstUnassignedLine == 0 {
					firstUnassignedLine = ln
				}
				lastUnassignedLine = ln
				unassignedTargetLines = append(unassignedTargetLines, ln)
			} else {
				unassignedTargetLines = append(unassignedTargetLines, 0)
			}
		}
	}

	if hasAdded && len(unassignedBlock) > 0 {
		chunks = append(chunks, Chunk{
			Name:        "overhead",
			Kind:        "overhead",
			Patch:       strings.Join(unassignedBlock, "\n"),
			StartLine:   firstUnassignedLine,
			EndLine:     lastUnassignedLine,
			TargetLines: unassignedTargetLines,
		})
	}

	return chunks, nil
}

// BuildChunksFromFullFile splits a patch into chunks based on AST nodes from the full source.
func BuildChunksFromFullFile(patch string, nodes []NodeCoord) ([]Chunk, error) {
	diff := ParsePatch(patch)
	if len(nodes) == 0 {
		return FallbackChunk(patch), nil
	}

	assigned := make([]bool, len(diff.DiffLines))
	var chunks []Chunk

	for _, n := range nodes {
		var chunkLines []string
		var targetLines []int
		firstLine := 0
		lastLine := 0

		// Nodes are defined as 0-indexed [StartRow, EndRow].
		// Target lines are 1-indexed.
		nodeStart := n.StartRow + 1
		nodeEnd := n.EndRow + 1

		// Check every line in the diff
		for i, diffLine := range diff.DiffLines {
			if assigned[i] {
				continue
			}
			targetLine, ok := diff.DiffToTargetLine[i]
			if !ok || targetLine == 0 {
				continue
			}

			isMatch := false
			// Heuristic for matching removal lines to AST nodes when we only have NEW file context:
			// If it's a removal (-) it maps to targetLine (the line number it *was* at in the new file's coordinate system).
			// If that targetLine is the start of a node, it's likely the header being replaced.
			// If it's within the node range, it's a change inside the node.
			if targetLine >= nodeStart && targetLine <= nodeEnd {
				isMatch = true
			} else if strings.HasPrefix(diffLine, "-") && targetLine == nodeStart {
				isMatch = true
			}

			if isMatch {
				chunkLines = append(chunkLines, diffLine)
				assigned[i] = true
				if firstLine == 0 {
					firstLine = targetLine
				}
				lastLine = targetLine
				targetLines = append(targetLines, targetLine)
			}
		}

		if len(chunkLines) > 0 {
			chunks = append(chunks, Chunk{
				Name:        n.Name,
				Kind:        n.Kind,
				Patch:       strings.Join(chunkLines, "\n"),
				StartLine:   firstLine,
				EndLine:     lastLine,
				TargetLines: targetLines,
			})
		}
	}

	// Create overhead chunk for unassigned lines that relate to additions
	var unassignedBlock []string
	var unassignedTargetLines []int
	hasAdded := false
	firstUnassignedLine := 0
	lastUnassignedLine := 0
	for j, a := range assigned {
		line := diff.DiffLines[j]
		if strings.HasPrefix(line, "@@") || strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if !a {
			unassignedBlock = append(unassignedBlock, line)
			if strings.HasPrefix(line, "+") {
				hasAdded = true
			}
			if ln, ok := diff.DiffToTargetLine[j]; ok {
				if firstUnassignedLine == 0 {
					firstUnassignedLine = ln
				}
				lastUnassignedLine = ln
				unassignedTargetLines = append(unassignedTargetLines, ln)
			} else {
				unassignedTargetLines = append(unassignedTargetLines, 0)
			}
		}
	}

	if hasAdded && len(unassignedBlock) > 0 {
		chunks = append(chunks, Chunk{
			Name:        "overhead",
			Kind:        "overhead",
			Patch:       strings.Join(unassignedBlock, "\n"),
			StartLine:   firstUnassignedLine,
			EndLine:     lastUnassignedLine,
			TargetLines: unassignedTargetLines,
		})
	}

	if len(chunks) == 0 {
		return FallbackChunk(patch), nil
	}

	return chunks, nil
}

// FallbackChunk returns the whole diff as a single chunk.
func FallbackChunk(patch string) []Chunk {
	diff := ParsePatch(patch)
	firstLine := 0
	lastLine := 0
	var targetLines []int
	for j := 0; j < len(diff.DiffLines); j++ {
		if ln, ok := diff.DiffToTargetLine[j]; ok {
			if firstLine == 0 {
				firstLine = ln
			}
			lastLine = ln
			targetLines = append(targetLines, ln)
		} else {
			targetLines = append(targetLines, 0)
		}
	}
	return []Chunk{
		{
			Name:        "file",
			Kind:        "file",
			Patch:       patch,
			StartLine:   firstLine,
			EndLine:     lastLine,
			TargetLines: targetLines,
		},
	}
}
