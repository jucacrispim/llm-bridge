//go:build knowledge_onnx

package knowledge

import "testing"

// TestEmbedRequiresPaths asserts the ONNX embedder rejects empty model/tokenizer
// paths before it touches the shared library (so it works even when the C libs
// are not present at test time).
func TestEmbedRequiresPaths(t *testing.T) {
	if _, err := NewEmbedder("", ""); err == nil {
		t.Fatal("expected error for empty model/tokenizer paths")
	}
}
