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
// Application of the prefix is left to the caller/manager so the Embedder
// stays a thin, prefix-free interface.
const (
	QueryPrefix   = "query: "
	PassagePrefix = "passage: "
)

// Embedder turns text into a dense vector. It is the pluggable point for
// different embedding providers (ONNX-backed with the knowledge_onnx build
// tag, or the disabled stub otherwise).
type Embedder interface {
	// Embed returns the vector for text. isQuery indicates whether the text
	// is a search query (as opposed to a document to store); callers should
	// apply the appropriate E5 prefix before calling.
	Embed(text string, isQuery bool) ([]float32, error)
}
