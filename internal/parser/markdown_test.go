package parser

import (
	"strings"
	"testing"
)

func TestMarkdownParser_GetNodes(t *testing.T) {
	src := `This is some preamble content before the first heading.
It is multiline.

# Heading 1
This is text in section 1.
With multiple lines.

## Heading 1.1
Content under 1.1.

` + "```" + `go
# This is a comment/heading inside a code block
func foo() {}
` + "```" + `

# Heading 2
Some other content.
`

	p := &markdownParser{}
	nodes := p.getNodes(src)

	// Since we have a preamble, Heading 1, Heading 1.1, and Heading 2.
	// We expect 4 Flat nodes total.
	if len(nodes) != 4 {
		t.Fatalf("expected 4 nodes, got %d: %+v", len(nodes), nodes)
	}

	expected := []struct {
		name string
		kind string
	}{
		{name: "preamble", kind: "section"},
		{name: "Heading 1", kind: "section"},
		{name: "Heading 1.1", kind: "section"},
		{name: "Heading 2", kind: "section"},
	}

	for i, exp := range expected {
		if nodes[i].Name != exp.name {
			t.Errorf("node %d: expected name %q, got %q", i, exp.name, nodes[i].Name)
		}
		if nodes[i].Kind != exp.kind {
			t.Errorf("node %d: expected kind %q, got %q", i, exp.kind, nodes[i].Kind)
		}
	}

	// Verify headings inside fenced code block are IGNORED (i.e. we don't get "This is a comment/heading inside a code block" as a node)
	for _, n := range nodes {
		if strings.Contains(n.Name, "code block") {
			t.Errorf("found false positive heading inside code block: %s", n.Name)
		}
	}
}

func TestMarkdownParser_NoPreamble(t *testing.T) {
	src := `# Heading 1
Content under 1.
`
	p := &markdownParser{}
	nodes := p.getNodes(src)

	// No preamble as first heading is at row 0.
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "Heading 1" {
		t.Errorf("expected name 'Heading 1', got %q", nodes[0].Name)
	}
}

func TestMarkdownParser_SetextHeading(t *testing.T) {
	src := `My Setext Heading
=================
And some normal text.
`
	p := &markdownParser{}
	nodes := p.getNodes(src)

	// Goldmark should parse Setext headings as heading AST nodes!
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "My Setext Heading" {
		t.Errorf("expected 'My Setext Heading', got %q", nodes[0].Name)
	}
}

func TestMarkdownParser_ParseFull(t *testing.T) {
	fullSource := `This is preamble.

# First Section
Changes here.

# Second Section
Changes here also.
`
	// Modifying First Section and Second Section.
	patch := `@@ -3,3 +3,3 @@
-# First Section
-Changes here.
+# First Section
+Modified changes here.
@@ -6,3 +6,3 @@
-# Second Section
-Changes here also.
+# Second Section
+Modified changes here as well.
`

	p := &markdownParser{}
	chunks, err := p.ParseFull(fullSource, patch)
	if err != nil {
		t.Fatalf("ParseFull failed: %v", err)
	}

	// We should got relevant chunks for processed headings.
	// Since both are touched, both headings should be matched.
	foundFirst := false
	foundSecond := false

	for _, c := range chunks {
		if c.Kind == "section" {
			if c.Name == "First Section" {
				foundFirst = true
				if !strings.Contains(c.Patch, "Modified changes here.") {
					t.Errorf("First Section chunk patch doesn't contain modification")
				}
			} else if c.Name == "Second Section" {
				foundSecond = true
				if !strings.Contains(c.Patch, "Modified changes here as well.") {
					t.Errorf("Second Section chunk patch doesn't contain modification")
				}
			}
		}
	}

	if !foundFirst {
		t.Error("expected chunk for 'First Section'")
	}
	if !foundSecond {
		t.Error("expected chunk for 'Second Section'")
	}
}
