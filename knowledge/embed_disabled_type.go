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
