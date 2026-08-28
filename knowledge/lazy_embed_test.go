package knowledge

import (
	"errors"
	"testing"
)

// countingEmbedder records how many times it was constructed and Embed called,
// so tests can assert that a LazyEmbedder defers and caches the load.
type countingEmbedder struct {
	dims int
}

func (c countingEmbedder) Embed(text string, _ bool) ([]float32, error) {
	return make([]float32, c.dims), nil
}

func TestLazyEmbedderDefersLoad(t *testing.T) {
	calls := 0
	ctor := func() (Embedder, error) {
		calls++
		return countingEmbedder{dims: 4}, nil
	}
	l := newLazyEmbedderFrom(ctor)

	// Nothing should be constructed until the first Embed.
	if calls != 0 {
		t.Fatalf("expected 0 constructor calls before first Embed, got %d", calls)
	}

	if _, err := l.Embed("hello", true); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected constructor called exactly once, got %d", calls)
	}

	// Subsequent calls reuse the cached embedder — no new construction.
	if _, err := l.Embed("world", false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 1 {
		t.Fatalf("expected constructor still called once, got %d", calls)
	}
}

func TestNewLazyEmbedderDefersRealLoad(t *testing.T) {
	// NewLazyEmbedder must not touch the (heavy) real embedder at construction
	// time — only on the first Embed. We use empty paths so the underlying
	// NewEmbedder would fail if it were called eagerly, but the lazy wrapper
	// itself must not error until Embed is invoked.
	l := NewLazyEmbedder("", "")

	// Construction succeeds without loading anything (the wrapper holds no
	// error until Embed triggers the real load).
	if l.err != nil {
		t.Fatalf("NewLazyEmbedder should not load at construction, got err %v", l.err)
	}

	// The first Embed triggers the (failing) real load and surfaces its error,
	// which is non-nil in both the default and knowledge_onnx builds (empty
	// paths are rejected by NewEmbedder either way).
	if _, err := l.Embed("x", true); err == nil {
		t.Fatal("expected an error from Embed with empty model/tokenizer paths")
	}
}

func TestLazyEmbedderCachesFailure(t *testing.T) {
	calls := 0
	boom := errors.New("load failed")
	ctor := func() (Embedder, error) {
		calls++
		return nil, boom
	}
	l := newLazyEmbedderFrom(ctor)

	for i := 0; i < 3; i++ {
		_, err := l.Embed("x", true)
		if !errors.Is(err, boom) {
			t.Fatalf("call %d: want %v, got %v", i, boom, err)
		}
	}
	if calls != 1 {
		t.Fatalf("expected constructor called once (cached failure), got %d", calls)
	}
}
