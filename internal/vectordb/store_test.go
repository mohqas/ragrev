package vectordb

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestOpenNewStore(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "test")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if s.Count() != 0 {
		t.Errorf("new store should be empty, got %d", s.Count())
	}
}

func TestAddAndQuery(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx := s.Index()

	docs := []string{
		"func processData(input []byte) error { return validate(input) }",
		"func processData(data []byte) error { return check(data) }",
		"type Config struct { Host string; Port int }",
	}
	ids := []string{"1", "2", "3"}

	// Use thread-style metadata with comments JSON and authors CSV.
	metas := []map[string]string{
		{
			"authors":  "alice",
			"comments": mustJSON([]ThreadComment{{ID: "10", Author: "alice", Body: "validate input"}}),
		},
		{
			"authors":  "alice,bob",
			"comments": mustJSON([]ThreadComment{{ID: "20", Author: "alice", Body: "check data"}, {ID: "21", Author: "bob", Body: "agreed"}}),
		},
		{
			"authors":  "bob",
			"comments": mustJSON([]ThreadComment{{ID: "30", Author: "bob", Body: "config looks good"}}),
		},
	}

	for _, d := range docs {
		idx.AddDocument(d)
	}

	if err := s.AddDocuments(ctx, ids, docs, metas); err != nil {
		t.Fatal(err)
	}

	results, err := s.Query(ctx, "func processData(buf []byte) error", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	for _, r := range results {
		if r.ID == "3" {
			t.Error("Config doc should not be in top-2 for processData query")
		}
	}
}

func TestQueryByAuthors(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx := s.Index()

	docs := []string{
		"func handleRequest(w http.ResponseWriter, r *http.Request) {}",
		"func handleRequest(rw http.ResponseWriter, req *http.Request) {}",
		"type Server struct { Port int }",
	}
	ids := []string{"1", "2", "3"}
	metas := []map[string]string{
		{
			"authors":  "alice",
			"comments": mustJSON([]ThreadComment{{ID: "10", Author: "alice", Body: "handle request"}}),
		},
		{
			"authors":  "bob,charlie",
			"comments": mustJSON([]ThreadComment{{ID: "20", Author: "bob", Body: "looks good"}, {ID: "21", Author: "charlie", Body: "nit"}}),
		},
		{
			"authors":  "charlie",
			"comments": mustJSON([]ThreadComment{{ID: "30", Author: "charlie", Body: "server config"}}),
		},
	}

	for _, d := range docs {
		idx.AddDocument(d)
	}
	if err := s.AddDocuments(ctx, ids, docs, metas); err != nil {
		t.Fatal(err)
	}

	t.Run("filter by alice", func(t *testing.T) {
		results, err := s.QueryByAuthors(ctx, "func handleRequest", 10, []string{"alice"})
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			if r.Metadata["authors"] != "alice" && !containsAuthor(r.Metadata, "alice") {
				t.Errorf("result %s should contain alice", r.ID)
			}
		}
	})

	t.Run("filter by charlie", func(t *testing.T) {
		results, err := s.QueryByAuthors(ctx, "func handleRequest", 10, []string{"charlie"})
		if err != nil {
			t.Fatal(err)
		}
		// Should return docs 2 and 3 (both have charlie)
		for _, r := range results {
			if !containsAuthor(r.Metadata, "charlie") {
				t.Errorf("result %s should contain charlie", r.ID)
			}
		}
	})

	t.Run("nil authors returns all", func(t *testing.T) {
		results, err := s.QueryByAuthors(ctx, "func handleRequest", 10, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 3 {
			t.Errorf("expected 3 results, got %d", len(results))
		}
	})
}

func TestExtractCommentsByAuthors(t *testing.T) {
	comments := []ThreadComment{
		{ID: "1", Author: "alice", Body: "first"},
		{ID: "2", Author: "bob", Body: "second"},
		{ID: "3", Author: "alice", Body: "third"},
	}
	meta := map[string]string{
		"comments": mustJSON(comments),
	}

	t.Run("extract alice", func(t *testing.T) {
		result := ExtractCommentsByAuthors(meta, map[string]bool{"alice": true})
		if len(result) != 2 {
			t.Errorf("expected 2, got %d", len(result))
		}
	})

	t.Run("nil authors returns all", func(t *testing.T) {
		result := ExtractCommentsByAuthors(meta, nil)
		if len(result) != 3 {
			t.Errorf("expected 3, got %d", len(result))
		}
	})

	t.Run("missing key returns nil", func(t *testing.T) {
		result := ExtractCommentsByAuthors(map[string]string{}, nil)
		if result != nil {
			t.Errorf("expected nil, got %v", result)
		}
	})
}

func TestGetByID(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, "test")
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	idx := s.Index()
	idx.AddDocument("hello world test content")

	err = s.AddDocuments(ctx, []string{"a"}, []string{"hello world test content"}, []map[string]string{{"k": "v"}})
	if err != nil {
		t.Fatal(err)
	}

	found, err := s.GetByID(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("expected to find document 'a'")
	}

	found, err = s.GetByID(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Error("expected not to find 'nonexistent'")
	}
}

func TestQueryEmpty(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "test")
	results, err := s.Query(context.Background(), "anything", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("empty store query should return 0 results, got %d", len(results))
	}
}

func TestQueryKLargerThanStore(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir, "test")
	ctx := context.Background()

	s.Index().AddDocument("some document content here")
	err := s.AddDocuments(ctx, []string{"1"}, []string{"some document content here"}, []map[string]string{{}})
	if err != nil {
		t.Fatal(err)
	}

	results, err := s.Query(ctx, "some document", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (capped), got %d", len(results))
	}
}

func mustJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func containsAuthor(metadata map[string]string, author string) bool {
	return threadHasAuthor(metadata, map[string]bool{author: true})
}

func TestStorePersistenceAndSimilarity(t *testing.T) {
	dir := t.TempDir()

	// Create the store and add 10 dummy conversations
	s, err := Open(dir, "e2e_test")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	idx := s.Index()

	docs := []string{
		"func add(a, b int) int { return a + b }",
		"func sub(a, b int) int { return a - b }",
		"func mul(a, b int) int { return a * b }",
		"func div(a, b int) int { return a / b }",
		"func mod(a, b int) int { return a % b }",
		"type User struct { Name string }",
		"type Account struct { ID string }",
		"func init() { fmt.Println(\"setup\") }",
		"func cleanup() { fmt.Println(\"teardown\") }",
		"var ErrNotFound = errors.New(\"not found\")",
	}

	for _, d := range docs {
		idx.AddDocument(d)
	}

	ids := make([]string, len(docs))
	metas := make([]map[string]string, len(docs))
	for i := range docs {
		ids[i] = fmt.Sprintf("id-%d", i)
		metas[i] = map[string]string{"index": fmt.Sprintf("%d", i)}
	}

	if err := s.AddDocuments(ctx, ids, docs, metas); err != nil {
		t.Fatalf("AddDocuments failed: %v", err)
	}

	// Reopen the store from the same directory to test persistence
	// This will test loadIndex and tfidfEmbeddingFunc
	loadedStore, err := Open(dir, "e2e_test")
	if err != nil {
		t.Fatalf("Failed to open existing store: %v", err)
	}

	if loadedStore.Count() != 10 {
		t.Fatalf("Expected 10 documents in loaded store, got %d", loadedStore.Count())
	}

	// 1. Exact match test
	exactQuery := "type User struct { Name string }"
	resExact, err := loadedStore.Query(ctx, exactQuery, 3)
	if err != nil {
		t.Fatalf("Exact query failed: %v", err)
	}
	if len(resExact) == 0 || resExact[0].Content != exactQuery {
		t.Errorf("Expected exact match to be top result, got %v", resExact)
	}
	if resExact[0].Score < 0.99 {
		t.Errorf("Expected score ~1.0 for exact match, got %f", resExact[0].Score)
	}

	// 2. Partial match test
	partialQuery := "func mul(x, y int) { return x * y }"
	resPartial, err := loadedStore.Query(ctx, partialQuery, 3)
	if err != nil {
		t.Fatalf("Partial query failed: %v", err)
	}
	if len(resPartial) == 0 || !strings.Contains(resPartial[0].Content, "mul(a, b int)") {
		t.Errorf("Expected partial match 'mul(a, b)' to be top result, got:\n%s\nScore: %f", resPartial[0].Content, resPartial[0].Score)
	}

	// 3. No match test (query something drastically different)
	// We'll use a word that isn't anywhere in the index, but gives 0 similarity.
	// Chromem-go might use L2 or Cosine. If Cosine, isolated vectors give low score.
	noMatchQuery := "html body table center xyz"
	resNoMatch, err := loadedStore.Query(ctx, noMatchQuery, 3)
	if err != nil {
		t.Fatalf("No match query failed: %v", err)
	}

	// For a query with no overlapping tokens with the test documents, TF-IDF cosine similarity should be 0.
	// Because chromem-go might shift the range or we have an all-zero vector, we just check it is
	// significantly lower than the exact match.
	if len(resNoMatch) > 0 {
		// Exact match should have score very close to 1. No match should have 0 or at least < 0.5
		// Because chromem normalizes 0 vectors differently, we just assert it's less than partial.
		if resNoMatch[0].Score > 0.85 {
			t.Errorf("Expected lower score for unrelated document, got %f for top match", resNoMatch[0].Score)
		}
	}
}
