// Package knowledge implements a small local knowledge base for the bridge.
//
// It provides an embedder-agnostic index (brute-force cosine via normalized
// dot product) and, in later phases, persistence, seeding and the `knowledge`
// tool. The embedding provider is pluggable behind the Embedder interface
// (ONNX-backed with the knowledge_onnx build tag, or a stub that reports
// embeddings are disabled).
package knowledge

import (
	"math"
	"sort"
)

// Result is a single hit returned by Index.Search.
type Result struct {
	Label string  `json:"label"`
	Score float32 `json:"score"`
	Text  string  `json:"text"`
}

// Index is a vector store over normalized embeddings. Because vectors are
// normalized on insert, cosine similarity equals the dot product.
type Index interface {
	// Search returns the top-k vectors most similar to q (normalized).
	Search(q []float32, k int) []Result
	// Add inserts a vector under a label/text and returns its id.
	Add(vec []float32, label, text string) int
	// Len returns the number of stored vectors.
	Len() int
}

// bruteForce is a simple O(N*d) Index over normalized vectors. Good enough for
// the expected scale (tens to low hundreds of thousands of chunks); the Index
// interface exists so a fancier structure (e.g. HNSW) can be swapped in later.
type bruteForce struct {
	vecs  [][]float32
	label []string
	text  []string
}

// NewBruteForce returns an empty brute-force Index.
func NewBruteForce() Index {
	return &bruteForce{}
}

// Add normalizes vec and stores it.
func (b *bruteForce) Add(vec []float32, label, text string) int {
	normalize(vec)
	b.vecs = append(b.vecs, vec)
	b.label = append(b.label, label)
	b.text = append(b.text, text)
	return len(b.vecs) - 1
}

// Len returns the number of stored vectors.
func (b *bruteForce) Len() int { return len(b.vecs) }

// Search returns the top-k vectors closest to q by cosine (dot product on
// normalized vectors). q is normalized in place. k <= 0 falls back to 5.
func (b *bruteForce) Search(q []float32, k int) []Result {
	if k <= 0 {
		k = 5
	}
	normalize(q)

	type scored struct {
		idx   int
		score float32
	}
	hits := make([]scored, 0, len(b.vecs))
	for i, v := range b.vecs {
		hits = append(hits, scored{idx: i, score: dot(q, v)})
	}
	sort.Slice(hits, func(a, c int) bool {
		// Descending score; ties broken by insertion order for stability.
		if hits[a].score != hits[c].score {
			return hits[a].score > hits[c].score
		}
		return hits[a].idx < hits[c].idx
	})
	if len(hits) > k {
		hits = hits[:k]
	}
	out := make([]Result, 0, len(hits))
	for _, h := range hits {
		out = append(out, Result{
			Label: b.label[h.idx],
			Text:  b.text[h.idx],
			Score: h.score,
		})
	}
	return out
}

// normalize scales v to unit length in place. Zero vectors are left untouched.
func normalize(v []float32) {
	n := dot(v, v)
	if n <= 0 {
		return
	}
	inv := 1 / sqrt(n)
	for i := range v {
		v[i] *= inv
	}
}

// dot returns the dot product of a and b.
func dot(a, b []float32) float32 {
	var sum float32
	for i := range a {
		sum += a[i] * b[i]
	}
	return sum
}

// sqrt computes the square root of x.
func sqrt(x float32) float32 {
	return float32(math.Sqrt(float64(x)))
}
