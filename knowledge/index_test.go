package knowledge

import (
	"math"
	"testing"
)

// fakeEmbedder maps a fixed vocabulary to deterministic vectors, giving the
// index a way to compute cosine similarity without any ONNX dependency.
type fakeEmbedder struct {
	dim int
}

func (f fakeEmbedder) Embed(text string, _ bool) ([]float32, error) {
	vec := make([]float32, f.dim)
	for i := 0; i < f.dim; i++ {
		// Deterministic value derived from the rune and position, scaled by
		// the text length so similar texts are closer.
		var r rune
		if len(text) > 0 {
			r = rune(text[0])
		}
		vec[i] = float32(int(r)+i) / float32(len(text)+1)
	}
	return vec, nil
}

func TestBruteForceSearchTopK(t *testing.T) {
	idx := NewBruteForce()
	idx.Add([]float32{1, 0, 0}, "a", "alpha")
	idx.Add([]float32{0, 1, 0}, "b", "bravo")
	idx.Add([]float32{0, 0, 1}, "c", "charlie")

	res := idx.Search([]float32{0.9, 0.1, 0}, 1)
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	if res[0].Label != "a" {
		t.Errorf("want label a, got %s", res[0].Label)
	}
}

func TestBruteForceSearchDefaultK(t *testing.T) {
	idx := NewBruteForce()
	for i := 0; i < 10; i++ {
		vec := make([]float32, 2)
		vec[0] = float32(i)
		idx.Add(vec, string(rune('a'+i)), "")
	}
	// k <= 0 defaults to 5.
	res := idx.Search([]float32{1, 0}, 0)
	if len(res) != 5 {
		t.Fatalf("want 5 results, got %d", len(res))
	}
}

func TestBruteForceNormalization(t *testing.T) {
	idx := NewBruteForce()
	// Parallel and anti-parallel to the query.
	idx.Add([]float32{3, 4}, "same", "") // length 5, normalized to {0.6,0.8}
	idx.Add([]float32{-3, -4}, "opp", "") // opposite direction

	res := idx.Search([]float32{0.6, 0.8}, 2)
	if res[0].Label != "same" {
		t.Errorf("want same first, got %s", res[0].Label)
	}
	if res[1].Label != "opp" {
		t.Errorf("want opp second, got %s", res[1].Label)
	}
	// Opposite direction should have strongly negative similarity.
	if res[1].Score > -0.99 {
		t.Errorf("want score near -1 for opposite, got %f", res[1].Score)
	}
}

func TestBruteForceZeroVector(t *testing.T) {
	idx := NewBruteForce()
	idx.Add([]float32{0, 0}, "zero", "")
	idx.Add([]float32{1, 0}, "x", "")
	res := idx.Search([]float32{1, 0}, 2)
	// Zero vector normalizes to itself (unchanged); score is 0.
	if math.Abs(float64(res[1].Score)) > 1e-6 {
		t.Errorf("want score 0 for zero vector, got %f", res[1].Score)
	}
}

func TestFakeEmbedder(t *testing.T) {
	e := fakeEmbedder{dim: 4}
	v, err := e.Embed("hello", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 4 {
		t.Fatalf("want 4 dims, got %d", len(v))
	}
}
