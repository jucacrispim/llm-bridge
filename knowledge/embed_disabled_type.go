package knowledge

import "errors"

// DisabledEmbedder is a placeholder embedder that always fails, signaling the
// knowledge base feature is unavailable (either compiled out, or the ONNX
// embedder failed to load at runtime). It is kept in a file WITHOUT a build
// tag so it is available in both the default build and the knowledge_onnx
// build: cmd/bridge uses it as the fallback when no real embedder is available.
type DisabledEmbedder struct{}

// Embed always returns an error.
func (DisabledEmbedder) Embed(text string, isQuery bool) ([]float32, error) {
	return nil, errors.New("knowledge: embeddings disabled")
}
