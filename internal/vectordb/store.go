// Package vectordb wraps chromem-go to provide a persistent vector store
// with a pluggable embedding function (e.g. TF-IDF).
package vectordb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	chromem "github.com/philippgille/chromem-go"

	"github.com/mohqas/ragrev/internal/vectorizer"
)

// DefaultCollection is the default collection name.
const DefaultCollection = "reviewer_persona"

// QueryResult is a convenience wrapper around a chromem query result.
type QueryResult struct {
	ID       string
	Content  string
	Metadata map[string]string
	Score    float32
}

// ThreadComment mirrors github.ThreadComment for deserialization from metadata.
type ThreadComment struct {
	ID        string `json:"id"`
	Author    string `json:"author"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
}

// Store wraps a chromem-go persistent DB with a TF-IDF embedding function.
type Store struct {
	db         *chromem.DB
	collection *chromem.Collection
	index      *vectorizer.Index
	dbPath     string
}

// tfidfEmbeddingFunc returns a chromem.EmbeddingFunc backed by a TF-IDF index.
func tfidfEmbeddingFunc(idx *vectorizer.Index) chromem.EmbeddingFunc {
	return func(_ context.Context, text string) ([]float32, error) {
		return idx.Vectorize(text), nil
	}
}

// Open opens or creates a persistent vector store at dbPath.
// It uses a TF-IDF index for embeddings — no external model needed.
func Open(dbPath string, collectionName string) (*Store, error) {
	if collectionName == "" {
		collectionName = DefaultCollection
	}

	db, err := chromem.NewPersistentDB(dbPath, false)
	if err != nil {
		return nil, fmt.Errorf("opening chromem db: %w", err)
	}

	idx, err := loadIndex(dbPath)
	if err != nil {
		return nil, fmt.Errorf("loading tfidf index: %w", err)
	}

	ef := tfidfEmbeddingFunc(idx)
	// Do not call GetCollection with nil, it falls back to OpenAI embedder.
	col, err := db.GetOrCreateCollection(collectionName, nil, ef)
	if err != nil {
		return nil, fmt.Errorf("getting/creating collection: %w", err)
	}

	return &Store{
		db:         db,
		collection: col,
		index:      idx,
		dbPath:     dbPath,
	}, nil
}

// Index returns the underlying TF-IDF index so callers can register
// documents before adding them (necessary for accurate IDF weights).
func (s *Store) Index() *vectorizer.Index {
	return s.index
}

func indexFilePath(dbPath string) string {
	return dbPath + "/tfidf.json"
}

func loadIndex(dbPath string) (*vectorizer.Index, error) {
	p := indexFilePath(dbPath)
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return vectorizer.NewIndex(), nil
		}
		return nil, err
	}
	idx := vectorizer.NewIndex()
	if err := json.Unmarshal(data, idx); err != nil {
		return nil, err
	}
	return idx, nil
}

func saveIndex(dbPath string, idx *vectorizer.Index) error {
	data, err := json.Marshal(idx)
	if err != nil {
		return err
	}
	return os.WriteFile(indexFilePath(dbPath), data, 0644)
}

// Count returns the number of documents in the collection.
func (s *Store) Count() int {
	return s.collection.Count()
}

// Reset deletes the existing collection and creates a fresh empty one.
// This is used to mathematically re-evaluate vectors when new words are discovered.
func (s *Store) Reset(ctx context.Context) error {
	// Re-initialize index
	s.index = vectorizer.NewIndex()

	// Delete the existing collection from the DB
	if err := s.db.DeleteCollection(s.collection.Name); err != nil {
		return fmt.Errorf("deleting collection: %w", err)
	}

	// Delete the physical vocabulary cache
	_ = os.Remove(indexFilePath(s.dbPath))

	// Re-create the collection
	ef := tfidfEmbeddingFunc(s.index)
	col, err := s.db.CreateCollection(s.collection.Name, nil, ef)
	if err != nil {
		return fmt.Errorf("creating fresh collection: %w", err)
	}

	s.collection = col
	return nil
}

// AddDocuments adds a batch of documents to the collection.
// The caller should have already registered all documents with the TF-IDF
// index (via s.Index().AddDocument()) before calling this, so that IDF
// weights are accurate.
func (s *Store) AddDocuments(ctx context.Context, ids []string, contents []string, metadatas []map[string]string) error {
	if len(ids) == 0 {
		return nil
	}

	docs := make([]chromem.Document, len(ids))
	for i := range ids {
		vec := s.index.Vectorize(contents[i])
		docs[i] = chromem.Document{
			ID:        ids[i],
			Content:   contents[i],
			Metadata:  metadatas[i],
			Embedding: vec,
		}
	}

	err := s.collection.AddDocuments(ctx, docs, 1)
	if err != nil {
		return err
	}

	// Save TF-IDF state to disk so queries map to the same vector space later.
	// We extract the base path from one of the internal paths if needed, but
	// actually we don't store dbPath in Store. We need to add it!
	return saveIndex(s.dbPath, s.index)
}

// Query finds the top-k most similar documents to the given text.
func (s *Store) Query(ctx context.Context, text string, k int) ([]QueryResult, error) {
	return s.QueryByAuthors(ctx, text, k, nil)
}

// QueryByAuthors finds the top-k most similar documents, optionally filtering
// so that only threads containing comments from at least one of the given
// authors are returned. If authors is nil or empty, no filtering is applied.
//
// Because chromem-go's where filter only supports exact single-value matching,
// we use a whereDocument filter on the "authors" metadata field for single-author
// lookups, and post-filter for multi-author queries. When filtering by authors,
// we fetch extra results and then trim to k after filtering.
func (s *Store) QueryByAuthors(ctx context.Context, text string, k int, authors []string) ([]QueryResult, error) {
	count := s.collection.Count()
	if count == 0 {
		return nil, nil
	}

	if len(authors) == 0 {
		// No author filter — simple query.
		nResults := k
		if nResults > count {
			nResults = count
		}
		results, err := s.collection.Query(ctx, text, nResults, nil, nil)
		if err != nil {
			return nil, fmt.Errorf("querying collection: %w", err)
		}
		return toQueryResults(results), nil
	}

	// With author filtering: fetch more results than needed, then post-filter.
	// We request up to count results to ensure we find enough after filtering.
	nFetch := count

	results, err := s.collection.Query(ctx, text, nFetch, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("querying collection: %w", err)
	}

	authorSet := make(map[string]bool, len(authors))
	for _, a := range authors {
		authorSet[a] = true
	}

	var filtered []QueryResult
	for _, r := range results {
		if threadHasAuthor(r.Metadata, authorSet) {
			filtered = append(filtered, QueryResult{
				ID:       r.ID,
				Content:  r.Content,
				Metadata: r.Metadata,
				Score:    r.Similarity,
			})
			if len(filtered) >= k {
				break
			}
		}
	}
	return filtered, nil
}

// threadHasAuthor checks if a thread's metadata "authors" field contains
// any of the given authors, or if the "comments" JSON contains a matching author.
func threadHasAuthor(metadata map[string]string, authors map[string]bool) bool {
	// Check the comma-separated "authors" metadata field first (fast path).
	if authorsStr, ok := metadata["authors"]; ok {
		for _, a := range strings.Split(authorsStr, ",") {
			if authors[strings.TrimSpace(a)] {
				return true
			}
		}
	}

	// Fallback: parse the "comments" JSON and check individual authors.
	if commentsJSON, ok := metadata["comments"]; ok {
		var comments []ThreadComment
		if err := json.Unmarshal([]byte(commentsJSON), &comments); err == nil {
			for _, c := range comments {
				if authors[c.Author] {
					return true
				}
			}
		}
	}

	return false
}

// ExtractCommentsByAuthors parses the "comments" metadata JSON and returns
// only comments from the specified authors. If authors is nil/empty, returns all.
func ExtractCommentsByAuthors(metadata map[string]string, authors map[string]bool) []ThreadComment {
	commentsJSON, ok := metadata["comments"]
	if !ok {
		return nil
	}

	var all []ThreadComment
	if err := json.Unmarshal([]byte(commentsJSON), &all); err != nil {
		return nil
	}

	if len(authors) == 0 {
		return all
	}

	var filtered []ThreadComment
	for _, c := range all {
		if authors[c.Author] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func toQueryResults(results []chromem.Result) []QueryResult {
	out := make([]QueryResult, len(results))
	for i, r := range results {
		out[i] = QueryResult{
			ID:       r.ID,
			Content:  r.Content,
			Metadata: r.Metadata,
			Score:    r.Similarity,
		}
	}
	return out
}

// ExistingIDs returns the set of document IDs already in the collection.
// Note: chromem-go doesn't expose a list-all-IDs method, so for the
// persistent DB we track this via a side-channel. For now we return an
// empty set when loading from a fresh store — the caller uses GetByID
// to check individual IDs.
func (s *Store) GetByID(ctx context.Context, id string) (bool, error) {
	_, err := s.collection.GetByID(ctx, id)
	if err != nil {
		return false, nil // not found
	}
	return true, nil
}
