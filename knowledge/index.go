// Copyright 2026 Juca Crispim <juca@poraodojuca.dev>
//
// This file is part of llm-bridge.
//
// llm-bridge is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// llm-bridge is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with llm-bridge. If not, see <http://www.gnu.org/licenses/>.

// Package knowledge implements the bridge's small local knowledge base: a
// vector index, persistence, seeding and the `knowledge` tool. The embedding
// provider sits behind the Embedder interface.
//
// Internals are in docs/source/hacking/knowledge-internals.rst.
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

// Index is a vector store over normalized embeddings, so cosine similarity is
// the dot product.
type Index interface {
	// Search returns the top-k vectors most similar to q.
	Search(q []float32, k int) []Result
	// Add inserts a vector under a label/text and returns its id.
	Add(vec []float32, label, text string) int
	// Len returns the number of stored vectors.
	Len() int
}

// bruteForce is an O(N*d) Index over normalized vectors, enough for the scale
// of a KB. The interface exists so another structure can replace it.
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

// Search returns the top-k vectors closest to q by cosine. q is normalized in
// place; k <= 0 means 5.
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
