// Package vectorizer implements a pure-Go TF-IDF vectorizer.
// No ML model or external service required.
package vectorizer

import (
	"math"
	"strings"
	"unicode"
)

// Index maintains a TF-IDF vocabulary and document-frequency counts.
type Index struct {
	// Vocab maps token -> index in the vector.
	Vocab map[string]int `json:"vocab"`
	// DocFreq tracks how many documents each token appears in.
	DocFreq map[string]int `json:"doc_freq"`
	// NumDocs is the total number of documents added.
	NumDocs int `json:"num_docs"`
}

// NewIndex creates an empty TF-IDF index.
func NewIndex() *Index {
	return &Index{
		Vocab:   make(map[string]int),
		DocFreq: make(map[string]int),
	}
}

// Tokenize splits text into normalized tokens.
// It lowercases, strips diff prefixes (+/-), and splits on non-alphanumeric boundaries.
func Tokenize(text string) []string {
	var tokens []string
	for _, line := range strings.Split(text, "\n") {
		// Strip diff prefixes
		line = strings.TrimLeft(line, "+-")
		words := strings.FieldsFunc(line, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
		})
		for _, w := range words {
			w = strings.ToLower(w)
			if len(w) < 2 {
				continue
			}
			tokens = append(tokens, w)
		}
	}
	return tokens
}

// AddDocument registers a document's tokens in the index, updating
// vocabulary and document frequency. Call this for every document
// before calling Vectorize.
func (idx *Index) AddDocument(text string) {
	idx.NumDocs++
	seen := make(map[string]bool)
	for _, tok := range Tokenize(text) {
		// Add to vocabulary if new
		if _, ok := idx.Vocab[tok]; !ok {
			idx.Vocab[tok] = len(idx.Vocab)
		}
		// Count document frequency (once per doc per token)
		if !seen[tok] {
			seen[tok] = true
			idx.DocFreq[tok]++
		}
	}
}

// VocabSize returns the current vocabulary size (vector dimension).
func (idx *Index) VocabSize() int {
	return len(idx.Vocab)
}

// Vectorize produces a TF-IDF vector for the given text using the
// current vocabulary and IDF weights. Unknown tokens are ignored.
// The returned vector has length equal to VocabSize().
func (idx *Index) Vectorize(text string) []float32 {
	tokens := Tokenize(text)
	if len(tokens) == 0 || len(idx.Vocab) == 0 {
		return make([]float32, len(idx.Vocab))
	}

	// Term frequency
	tf := make(map[string]int)
	for _, tok := range tokens {
		tf[tok]++
	}

	vec := make([]float32, len(idx.Vocab))
	totalTokens := float64(len(tokens))

	for tok, count := range tf {
		pos, ok := idx.Vocab[tok]
		if !ok {
			continue // unknown token
		}
		// TF: normalized by document length
		termFreq := float64(count) / totalTokens
		// IDF: log(N / df), with smoothing
		df := float64(idx.DocFreq[tok])
		if df == 0 {
			df = 1
		}
		idf := math.Log(float64(idx.NumDocs+1) / (df + 1))
		vec[pos] = float32(termFreq * idf)
	}

	// L2 normalize
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	if norm > 0 {
		norm = math.Sqrt(norm)
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}

	return vec
}

// CosineSimilarity computes the cosine similarity between two vectors.
// Both vectors must have the same length.
func CosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return float32(dot / denom)
}

