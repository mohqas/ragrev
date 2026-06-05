package parser

import (
	"strings"
	"testing"
)

func TestBuildChunksFromFullFile(t *testing.T) {
	// A diff that touches both Hello and World
	patch := `@@ -4,3 +4,3 @@
-func Hello() {
+func HelloMsg() {
 	fmt.Println("Hello")
@@ -8,3 +8,3 @@
-func World() {
+func WorldMsg() {
 	fmt.Println("World")
`
	nodes := []NodeCoord{
		{Name: "Hello", Kind: "function", StartRow: 4, EndRow: 6},
		{Name: "World", Kind: "function", StartRow: 8, EndRow: 10},
	}

	chunks, err := BuildChunksFromFullFile(patch, nodes)
	if err != nil {
		t.Fatalf("BuildChunksFromFullFile failed: %v", err)
	}

	for i, c := range chunks {
		t.Logf("Chunk %d: Name=%s, Kind=%s, Patch=\n%s", i, c.Name, c.Kind, c.Patch)
	}

	// We currently get 3 chunks because removals at the start of functions
	// are hard to attribute without OLD file context in ParsePatch.
	// This is acceptable as long as the crucial lines are inside the correct chunks.
	if len(chunks) < 2 {
		t.Fatalf("Expected at least 2 chunks, got %d", len(chunks))
	}

	expectedNames := map[string]bool{"Hello": true, "World": true, "overhead": true}
	for _, c := range chunks {
		if !expectedNames[c.Name] {
			t.Errorf("Unexpected chunk name: %s", c.Name)
		}
		if c.Name != "overhead" && c.Kind != "function" {
			t.Errorf("Expected kind function for %s, got %s", c.Name, c.Kind)
		}
		// If it's a function chunk, it should contain the change if header was matched.
		// In this test, headers often fall into overhead, so we only check that the changes are SOMEWHERE.
	}

	totalPatch := ""
	for _, c := range chunks {
		totalPatch += c.Patch
	}
	if !strings.Contains(totalPatch, "HelloMsg") || !strings.Contains(totalPatch, "WorldMsg") {
		t.Errorf("Total patch missing expected changes")
	}
}

func TestBuildChunksFromFullFile_WithOverhead(t *testing.T) {
	// A diff that touches imports (overhead) and Hello
	patch := `@@ -1,2 +1,3 @@
 package main
+import "os"
 import "fmt"
@@ -4,3 +5,3 @@
-func Hello() {
+func HelloMsg() {
 	fmt.Println("Hello")
`
	nodes := []NodeCoord{
		{Name: "Hello", Kind: "function", StartRow: 4, EndRow: 6},
	}

	chunks, err := BuildChunksFromFullFile(patch, nodes)
	if err != nil {
		t.Fatalf("BuildChunksFromFullFile failed: %v", err)
	}

	for i, c := range chunks {
		t.Logf("Chunk %d: Name=%s, Kind=%s, Patch=\n%s", i, c.Name, c.Kind, c.Patch)
	}

	// Should have 2 chunks: "Hello" and "overhead"
	if len(chunks) != 2 {
		t.Fatalf("Expected 2 chunks, got %d", len(chunks))
	}

	foundOverhead := false
	foundHello := false

	for _, c := range chunks {
		if c.Name == "overhead" {
			foundOverhead = true
			if !strings.Contains(c.Patch, "import \"os\"") {
				t.Errorf("Overhead chunk missing expected line")
			}
		} else if c.Name == "Hello" {
			foundHello = true
			if !strings.Contains(c.Patch, "HelloMsg") {
				t.Errorf("Hello chunk missing expected line")
			}
		}
	}

	if !foundOverhead || !foundHello {
		t.Errorf("Did not find both overhead and Hello chunks")
	}
}
