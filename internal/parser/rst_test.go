package parser

import (
	"strings"
	"testing"
)

// ── getNodes unit tests ───────────────────────────────────────────────────────

func TestRSTGetNodes_BasicSections(t *testing.T) {
	src := `First Section
=============
Some content here.
More content.

Second Section
==============
Other content.
`
	p := &rstParser{}
	nodes := p.getNodes(src)

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "First Section" || nodes[0].Kind != "section" {
		t.Errorf("node 0: got name=%q kind=%q", nodes[0].Name, nodes[0].Kind)
	}
	if nodes[1].Name != "Second Section" || nodes[1].Kind != "section" {
		t.Errorf("node 1: got name=%q kind=%q", nodes[1].Name, nodes[1].Kind)
	}
	// First section: startRow=0 (title line), ends before Second Section
	if nodes[0].StartRow != 0 {
		t.Errorf("node 0 StartRow: want 0, got %d", nodes[0].StartRow)
	}
	// Second section starts at row 5 (0-based): blank line, then "Second Section"
	if nodes[1].StartRow != 5 {
		t.Errorf("node 1 StartRow: want 5, got %d", nodes[1].StartRow)
	}
	// First section EndRow should be one before Second Section startRow
	if nodes[0].EndRow != nodes[1].StartRow-1 {
		t.Errorf("node 0 EndRow: want %d, got %d", nodes[1].StartRow-1, nodes[0].EndRow)
	}
}

func TestRSTGetNodes_Overline(t *testing.T) {
	// "Document Title" is 14 chars — adornment must be >= 14.
	src := `==============
Document Title
==============
Some intro text.

A Section
---------
Body text.
`
	p := &rstParser{}
	nodes := p.getNodes(src)

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "Document Title" {
		t.Errorf("node 0 name: want 'Document Title', got %q", nodes[0].Name)
	}
	// With overline, startRow should be 0 (the overline line).
	if nodes[0].StartRow != 0 {
		t.Errorf("node 0 StartRow: want 0 (overline), got %d", nodes[0].StartRow)
	}
	if nodes[1].Name != "A Section" {
		t.Errorf("node 1 name: want 'A Section', got %q", nodes[1].Name)
	}
}

func TestRSTGetNodes_MixedAdornments(t *testing.T) {
	src := `Chapter One
===========
Chapter content.

Sub-section
-----------
Sub content.

Sub-sub
~~~~~~~
Deep content.
`
	p := &rstParser{}
	nodes := p.getNodes(src)

	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(nodes), nodes)
	}
	names := []string{"Chapter One", "Sub-section", "Sub-sub"}
	for i, name := range names {
		if nodes[i].Name != name {
			t.Errorf("node %d: want %q, got %q", i, name, nodes[i].Name)
		}
	}
}

func TestRSTGetNodes_Preamble(t *testing.T) {
	src := `This is introductory prose before any heading.
It spans multiple lines.

First Heading
=============
Content.
`
	p := &rstParser{}
	nodes := p.getNodes(src)

	if len(nodes) != 2 {
		t.Fatalf("expected 2 nodes (preamble + section), got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "preamble" {
		t.Errorf("node 0: want 'preamble', got %q", nodes[0].Name)
	}
	if nodes[0].StartRow != 0 {
		t.Errorf("preamble StartRow: want 0, got %d", nodes[0].StartRow)
	}
	if nodes[1].Name != "First Heading" {
		t.Errorf("node 1: want 'First Heading', got %q", nodes[1].Name)
	}
}

func TestRSTGetNodes_NoPreamble(t *testing.T) {
	// Blank lines before the first heading should NOT produce a preamble node.
	src := `
   

First Heading
=============
Content.
`
	p := &rstParser{}
	nodes := p.getNodes(src)

	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (no preamble), got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "First Heading" {
		t.Errorf("expected 'First Heading', got %q", nodes[0].Name)
	}
}

func TestRSTGetNodes_EmptyFile(t *testing.T) {
	p := &rstParser{}
	nodes := p.getNodes("")
	if nodes != nil {
		t.Errorf("expected nil nodes for empty file, got %+v", nodes)
	}
}

func TestRSTGetNodes_NoSections(t *testing.T) {
	src := `This is just prose.
No headings anywhere.
Just text.
`
	p := &rstParser{}
	nodes := p.getNodes(src)
	if nodes != nil {
		t.Errorf("expected nil nodes for prose-only file, got %+v", nodes)
	}
}

func TestRSTGetNodes_ShortUnderline(t *testing.T) {
	// Underline shorter than title → NOT a valid heading.
	src := `A Very Long Title
===
Some text.
`
	p := &rstParser{}
	nodes := p.getNodes(src)
	if nodes != nil {
		t.Errorf("expected nil nodes (short underline), got %+v", nodes)
	}
}

func TestRSTGetNodes_SingleChar(t *testing.T) {
	// Single character title with single character underline — valid.
	src := `X
=
Content.
`
	p := &rstParser{}
	nodes := p.getNodes(src)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node, got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "X" {
		t.Errorf("expected name 'X', got %q", nodes[0].Name)
	}
}

func TestRSTGetNodes_WindowsLineEndings(t *testing.T) {
	src := "Section One\r\n===========\r\nContent.\r\n"
	p := &rstParser{}
	nodes := p.getNodes(src)
	if len(nodes) != 1 {
		t.Fatalf("expected 1 node (CRLF), got %d: %+v", len(nodes), nodes)
	}
	if nodes[0].Name != "Section One" {
		t.Errorf("expected 'Section One', got %q", nodes[0].Name)
	}
}

// ── Parse() integration tests ─────────────────────────────────────────────────

func TestRSTParse_SmallDiff(t *testing.T) {
	// Patch that touches a line inside the second section.
	patch := `@@ -6,3 +6,3 @@
 
 Second Section
 ==============
-Old content.
+New content.
`
	p := &rstParser{}
	chunks, err := p.Parse(patch)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	// At least one chunk should contain the changed line.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Patch, "New content.") {
			found = true
		}
	}
	if !found {
		t.Error("no chunk contains the changed line")
	}
}

func TestRSTParse_FallbackOnNoParse(t *testing.T) {
	// Patch with no RST headings in the reconstructed source → fallback chunk.
	patch := `@@ -1,3 +1,3 @@
 Just prose line one.
-Old line.
+New line.
 Just prose line three.
`
	p := &rstParser{}
	chunks, err := p.Parse(patch)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("expected 1 fallback chunk, got %d", len(chunks))
	}
	if chunks[0].Kind != "file" {
		t.Errorf("expected fallback chunk kind 'file', got %q", chunks[0].Kind)
	}
}

// ── ParseFull() integration tests ─────────────────────────────────────────────

func TestRSTParseFullFile(t *testing.T) {
	fullSource := `Preamble line.

First Section
=============
Unchanged content.

Second Section
==============
Changed content.
`
	// fullSource line numbers (1-indexed):
	// 1: "Preamble line."
	// 2: ""
	// 3: "First Section"
	// 4: "============="
	// 5: "Unchanged content."
	// 6: ""
	// 7: "Second Section"
	// 8: "=============="
	// 9: "Changed content."
	patch := `@@ -7,3 +7,3 @@
 Second Section
 ==============
-Changed content.
+Updated content.
`
	p := &rstParser{}
	chunks, err := p.ParseFull(fullSource, patch)
	if err != nil {
		t.Fatalf("ParseFull failed: %v", err)
	}

	found := false
	for _, c := range chunks {
		if c.Name == "Second Section" && c.Kind == "section" {
			found = true
			if !strings.Contains(c.Patch, "Updated content.") {
				t.Error("Second Section chunk missing updated content")
			}
		}
	}
	if !found {
		t.Errorf("no chunk for 'Second Section'; chunks: %+v", chunks)
	}
}

func TestRSTParseFullFile_MultiSection(t *testing.T) {
	fullSource := `First Section
=============
Content A.

Second Section
==============
Content B.
`
	// Both sections touched.
	patch := `@@ -2,2 +2,2 @@
-Content A.
+Modified A.
@@ -6,2 +6,2 @@
-Content B.
+Modified B.
`
	p := &rstParser{}
	chunks, err := p.ParseFull(fullSource, patch)
	if err != nil {
		t.Fatalf("ParseFull failed: %v", err)
	}

	foundFirst := false
	foundSecond := false
	for _, c := range chunks {
		if c.Kind == "section" {
			switch c.Name {
			case "First Section":
				foundFirst = true
				if !strings.Contains(c.Patch, "Modified A.") {
					t.Error("First Section chunk missing modification")
				}
			case "Second Section":
				foundSecond = true
				if !strings.Contains(c.Patch, "Modified B.") {
					t.Error("Second Section chunk missing modification")
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

func TestRSTParseFullFile_Overhead(t *testing.T) {
	fullSource := `.. meta::
   :description: Overview doc

First Section
=============
Content.
`
	// Patch only touches the preamble/directive area.
	patch := `@@ -1,2 +1,2 @@
 .. meta::
-   :description: Overview doc
+   :description: Updated overview doc
`
	p := &rstParser{}
	chunks, err := p.ParseFull(fullSource, patch)
	if err != nil {
		t.Fatalf("ParseFull failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}
	// The change is in the preamble; expect a preamble or overhead chunk.
	found := false
	for _, c := range chunks {
		if strings.Contains(c.Patch, "Updated overview doc") {
			found = true
		}
	}
	if !found {
		t.Error("no chunk contains the preamble change")
	}
}
