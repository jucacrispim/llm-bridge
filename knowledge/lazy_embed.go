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

// LazyEmbedder defers the model load to the first Embed call, which keeps the
// bridge's boot fast. The constructor runs at most once, and a failure is
// cached, so a broken KB fails consistently instead of retrying the slow load.
type LazyEmbedder struct {
	ctor func() (Embedder, error)
	once sync.Once
	emb  Embedder
	err  error
}

// NewLazyEmbedder returns a LazyEmbedder over the ONNX model at
// modelPath/tokenizerPath.
func NewLazyEmbedder(modelPath, tokenizerPath string) *LazyEmbedder {
	return &LazyEmbedder{
		ctor: func() (Embedder, error) {
			return NewEmbedder(modelPath, tokenizerPath)
		},
	}
}

// newLazyEmbedderFrom is the constructor tests use to inject a fake.
func newLazyEmbedderFrom(ctor func() (Embedder, error)) *LazyEmbedder {
	return &LazyEmbedder{ctor: ctor}
}

// Embed loads the underlying embedder on first use and delegates. A failed load
// is cached, so a broken KB keeps failing the same way.
func (l *LazyEmbedder) Embed(text string, isQuery bool) ([]float32, error) {
	l.once.Do(func() {
		l.emb, l.err = l.ctor()
	})
	if l.err != nil {
		return nil, l.err
	}
	return l.emb.Embed(text, isQuery)
}
