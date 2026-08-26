//go:build !knowledge_onnx

package knowledge

import "testing"

// TestEmbedDisabled asserts that, without the knowledge_onnx tag, the
// DisabledEmbedder stub is returned and always errors. (It lives in its own
// !knowledge_onnx file because DisabledEmbedder only exists in that build.)
func TestEmbedDisabled(t *testing.T) {
	_, err := NewEmbedder("", "")
	if err == nil {
		t.Fatal("expected error from DisabledEmbedder")
	}
	if _, err := (DisabledEmbedder{}).Embed("x", true); err == nil {
		t.Fatal("expected error from DisabledEmbedder.Embed")
	}
}
