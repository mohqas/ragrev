package vectorizer

import (
	"math"
	"testing"
)

func TestTokenize(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int // minimum expected token count
	}{
		{"simple", "hello world", 2},
		{"diff line", "+func main() {", 2}, // "func", "main"
		{"removal line", "-old_func()", 1},  // "old_func" (underscore preserved)
		{"empty", "", 0},
		{"short tokens filtered", "a b c", 0}, // all < 2 chars
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Tokenize(tt.input)
			if len(got) < tt.want {
				t.Errorf("Tokenize(%q) = %v (len %d), want at least %d tokens", tt.input, got, len(got), tt.want)
			}
		})
	}
}

func TestIndex_AddAndVectorize(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("func main() { fmt.Println(\"hello\") }")
	idx.AddDocument("func helper() { return nil }")

	if idx.NumDocs != 2 {
		t.Errorf("NumDocs = %d, want 2", idx.NumDocs)
	}
	if idx.VocabSize() == 0 {
		t.Fatal("VocabSize = 0 after adding documents")
	}

	vec := idx.Vectorize("func main() { fmt.Println(\"hello\") }")
	if len(vec) != idx.VocabSize() {
		t.Errorf("vector length = %d, want %d", len(vec), idx.VocabSize())
	}

	// Vector should not be all zeros
	allZero := true
	for _, v := range vec {
		if v != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("vector is all zeros for a known document")
	}
}

func TestSimilarDocumentsScoreHigher(t *testing.T) {
	idx := NewIndex()

	doc1 := "func processData(input []byte) error { return validate(input) }"
	doc2 := "func processData(data []byte) error { return check(data) }"
	doc3 := "type Config struct { Host string; Port int }"

	idx.AddDocument(doc1)
	idx.AddDocument(doc2)
	idx.AddDocument(doc3)

	v1 := idx.Vectorize(doc1)
	v2 := idx.Vectorize(doc2)
	v3 := idx.Vectorize(doc3)

	sim12 := CosineSimilarity(v1, v2)
	sim13 := CosineSimilarity(v1, v3)

	if sim12 <= sim13 {
		t.Errorf("similar docs should score higher: sim(doc1,doc2)=%f <= sim(doc1,doc3)=%f", sim12, sim13)
	}
}

func TestCosineSimilarity_Identical(t *testing.T) {
	a := []float32{1, 2, 3}
	sim := CosineSimilarity(a, a)
	if math.Abs(float64(sim)-1.0) > 1e-6 {
		t.Errorf("identical vectors should have similarity ~1.0, got %f", sim)
	}
}

func TestCosineSimilarity_Orthogonal(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{0, 1, 0}
	sim := CosineSimilarity(a, b)
	if math.Abs(float64(sim)) > 1e-6 {
		t.Errorf("orthogonal vectors should have similarity ~0, got %f", sim)
	}
}

func TestCosineSimilarity_DifferentLengths(t *testing.T) {
	a := []float32{1, 2}
	b := []float32{1, 2, 3}
	sim := CosineSimilarity(a, b)
	if sim != 0 {
		t.Errorf("different length vectors should return 0, got %f", sim)
	}
}

func TestVectorize_EmptyDoc(t *testing.T) {
	idx := NewIndex()
	idx.AddDocument("some content here")
	vec := idx.Vectorize("")
	for i, v := range vec {
		if v != 0 {
			t.Errorf("empty doc vector[%d] = %f, want 0", i, v)
		}
	}
}


