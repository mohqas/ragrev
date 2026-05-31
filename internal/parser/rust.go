package parser

import (
	"context"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/rust"
)

type rsParser struct{}

func init() {
	Register(".rs", &rsParser{})
}

func parseRustNodeName(child *sitter.Node, src string) string {
	name := "unknown"
	if child.Type() == "impl_item" {
		for j := 0; j < int(child.ChildCount()); j++ {
			c := child.Child(j)
			if c != nil && c.Type() == "type_identifier" {
				name = "impl " + c.Content([]byte(src))
				break
			}
		}
		return name
	}

	for j := 0; j < int(child.ChildCount()); j++ {
		c := child.Child(j)
		if c != nil && (c.Type() == "identifier" || c.Type() == "type_identifier") {
			name = c.Content([]byte(src))
			break
		}
	}
	return name
}

func (p *rsParser) Parse(patch string) ([]Chunk, error) {
	diff := ParsePatch(patch)
	if len(diff.SourceLines) == 0 {
		return FallbackChunk(patch), nil
	}
	src := strings.Join(diff.SourceLines, "\n")

	parser := sitter.NewParser()
	parser.SetLanguage(rust.GetLanguage())

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
		t := child.Type()
		if t == "function_item" || t == "impl_item" || t == "struct_item" || t == "enum_item" || t == "trait_item" {
			kind := strings.Split(t, "_")[0]
			nodes = append(nodes, NodeCoord{
				Name:     parseRustNodeName(child, src),
				Kind:     kind,
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
