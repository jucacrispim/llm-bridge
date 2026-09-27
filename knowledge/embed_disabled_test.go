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
