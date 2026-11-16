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

// E5-style prefixes. The multilingual-e5-small model expects a `query: `
// prefix on search inputs and a `passage: ` prefix on stored documents.
const (
	QueryPrefix   = "query: "
	PassagePrefix = "passage: "
)

// Embedder turns text into a dense vector. It is the seam for a different
// embedding provider: ONNX with the knowledge_onnx build tag, a stub without.
type Embedder interface {
	// Embed returns the vector for text. isQuery says whether the text is a
	// query or a document to store; the caller applies the E5 prefix.
	Embed(text string, isQuery bool) ([]float32, error)
}
