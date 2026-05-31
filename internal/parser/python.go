package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/python"
)

type pyParser struct{}

func init() {
	Register(".py", &pyParser{})
}

func (p *pyParser) Parse(patch string) ([]Chunk, error) {
	diff := ParsePatch(patch)
	if len(diff.SourceLines) == 0 {
		return FallbackChunk(patch), nil
	}
	src := strings.Join(diff.SourceLines, "\n")

	parser := sitter.NewParser()
	parser.SetLanguage(python.GetLanguage())

	tree, err := parser.ParseCtx(context.Background(), nil, []byte(src))
	if err != nil || tree == nil {
		return FallbackChunk(patch), nil
	}

	var nodes []NodeCoord
	root := tree.RootNode()

	for i := 0; i < int(root.ChildCount()); i++ {
		child := root.Child(i)
		if child == nil {
			continue
		}
		if child.Type() == "function_definition" || child.Type() == "class_definition" {
			name := "unknown"
			for j := 0; j < int(child.ChildCount()); j++ {
				c := child.Child(j)
				if c != nil && c.Type() == "identifier" {
					name = c.Content([]byte(src))
					break
				}
			}
			nodes = append(nodes, NodeCoord{
				Name:     name,
				Kind:     strings.Split(child.Type(), "_")[0],
				StartRow: int(child.StartPoint().Row),
				EndRow:   int(child.EndPoint().Row),
			})
		}
	}

	if len(nodes) == 0 {
		return FallbackChunk(patch), nil
	}

	chunks, err := BuildChunks(diff, nodes)
	if err != nil {
		return FallbackChunk(patch), nil
	}
	return chunks, nil
}
