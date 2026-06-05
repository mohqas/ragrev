package parser

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

type goParser struct{}

func init() {
	Register(".go", &goParser{})
}

func (p *goParser) Parse(patch string) ([]Chunk, error) {
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

func (p *goParser) ParseFull(fullSource string, patch string) ([]Chunk, error) {
	nodes := p.getNodes(fullSource)
	return BuildChunksFromFullFile(patch, nodes)
}

func (p *goParser) getNodes(src string) []NodeCoord {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, parser.AllErrors)
	if err != nil && file == nil {
		return nil
	}

	var nodes []NodeCoord

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			start := fset.Position(d.Pos()).Line - 1
			end := fset.Position(d.End()).Line - 1
			name := d.Name.Name
			if d.Recv != nil && len(d.Recv.List) > 0 {
				if id, ok := d.Recv.List[0].Type.(*ast.Ident); ok {
					name = id.Name + "." + name
				} else if star, ok := d.Recv.List[0].Type.(*ast.StarExpr); ok {
					if id, ok := star.X.(*ast.Ident); ok {
						name = id.Name + "." + name
					}
				}
			}
			nodes = append(nodes, NodeCoord{
				Name:     name,
				Kind:     "function",
				StartRow: start,
				EndRow:   end,
			})
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				start := fset.Position(d.Pos()).Line - 1
				end := fset.Position(d.End()).Line - 1
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						nodes = append(nodes, NodeCoord{
							Name:     ts.Name.Name,
							Kind:     "type",
							StartRow: start,
							EndRow:   end,
						})
					}
				}
			}
		}
	}
	return nodes
}
