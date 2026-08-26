//go:build !knowledge_onnx

package knowledge

import "errors"

// DisabledEmbedder is the default embedder when the knowledge_onnx build tag
// is not set (i.e. a plain `make build`). It always fails, signaling that the
// knowledge base feature is compiled out.
type DisabledEmbedder struct{}

// NewEmbedder returns a DisabledEmbedder, since the ONNX-backed embedder is
// only compiled in with the knowledge_onnx tag.
func NewEmbedder(modelPath, tokenizerPath string) (Embedder, error) {
	return DisabledEmbedder{}, errors.New("knowledge: embeddings disabled (build without knowledge_onnx tag)")
}

// Embed always returns an error.
func (DisabledEmbedder) Embed(text string, isQuery bool) ([]float32, error) {
	return nil, errors.New("knowledge: embeddings disabled (build without knowledge_onnx tag)")
}
