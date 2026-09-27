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

import "sync"

// LazyEmbedder wraps an embedder constructor so the (expensive) model load is
// deferred until the first Embed call, instead of at startup. This keeps the
// bridge's boot fast: the ONNX model/tokenizer are only loaded when a
// `knowledge` tool call first actually needs an embedding.
//
// The constructor is invoked at most once (guarded by sync.Once) and the
// resulting embedder is reused for all subsequent calls. If the constructor
// fails (e.g. the binary was built without the knowledge_onnx tag, or the ONNX
// runtime/model files are missing), the error is surfaced as the result of the
// first knowledge operation rather than crashing the server — so a disabled KB
// is indistinguishable from a slow one until the model is actually used.
type LazyEmbedder struct {
	ctor func() (Embedder, error)
	once sync.Once
	emb  Embedder
	err  error
}

// NewLazyEmbedder returns a LazyEmbedder that defers loading the ONNX model at
// modelPath/tokenizerPath until the first Embed. An empty path set is only
// rejected at first use (matching the lazy contract); pass valid paths to get a
// working KB.
func NewLazyEmbedder(modelPath, tokenizerPath string) *LazyEmbedder {
	return &LazyEmbedder{
		ctor: func() (Embedder, error) {
			return NewEmbedder(modelPath, tokenizerPath)
		},
	}
}

// newLazyEmbedderFrom is the testable constructor used by tests to inject a
// fake (and count invocations). It is unexported to keep the public surface
// minimal.
func newLazyEmbedderFrom(ctor func() (Embedder, error)) *LazyEmbedder {
	return &LazyEmbedder{ctor: ctor}
}

// Embed loads the underlying embedder on first use and delegates. The result of
// a failed load is cached, so a broken KB fails consistently instead of
// re-attempting the (slow) load on every call.
func (l *LazyEmbedder) Embed(text string, isQuery bool) ([]float32, error) {
	l.once.Do(func() {
		l.emb, l.err = l.ctor()
	})
	if l.err != nil {
		return nil, l.err
	}
	return l.emb.Embed(text, isQuery)
}
