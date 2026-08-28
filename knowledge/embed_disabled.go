//go:build !knowledge_onnx

package knowledge

import "errors"

// NewEmbedder returns a DisabledEmbedder, since the ONNX-backed embedder is
// only compiled in with the knowledge_onnx tag. The DisabledEmbedder type
// itself lives in embed_disabled_type.go (no build tag) so it is also
// available to cmd/bridge in the knowledge_onnx build.
func NewEmbedder(modelPath, tokenizerPath string) (Embedder, error) {
	return DisabledEmbedder{}, errors.New("knowledge: embeddings disabled (build without knowledge_onnx tag)")
}
