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
