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

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSeed(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureSeededFirstLoad(t *testing.T) {
	root := t.TempDir()
	writeSeed(t, root, "global1.md", "conteudo global um")
	writeSeed(t, root, "global2.md", "conteudo global dois")
	writeSeed(t, root, "llm-bridge/proj1.md", "conteudo do projeto")

	m := NewManager(fakeEmbedder{dim: 8}, "llm-bridge")
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	// Global (2) + project (1).
	if m.Len() != 3 {
		t.Fatalf("want 3 seeded items, got %d", m.Len())
	}
}

// TestEnsureSeededGlobalOnly verifies that a manager whose seed project is
// empty (the shared global KB) takes only the global seed files and ignores the
// per-project subdirectory.
func TestEnsureSeededGlobalOnly(t *testing.T) {
	root := t.TempDir()
	writeSeed(t, root, "global.md", "conteudo global")
	writeSeed(t, root, "_global/inner.md", "nao deve entrar")
	writeSeed(t, root, "proj/inner.md", "nao deve entrar")

	m := NewManager(fakeEmbedder{dim: 8}, GlobalKey)
	m.SetSeedProject("")
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 1 {
		t.Fatalf("want 1 item (global seed only), got %d", m.Len())
	}
	res, err := m.Search("conteudo global", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Label != "seed:global.md" {
		t.Fatalf("want the global seed, got %+v", res)
	}
}

// TestEnsureSeededProjectScoped verifies that the seed project is decoupled
// from the storage key: a manager stored under one key can read another
// project's seed subdirectory.
func TestEnsureSeededProjectScoped(t *testing.T) {
	root := t.TempDir()
	writeSeed(t, root, "global.md", "conteudo global")
	writeSeed(t, root, "realproj/inner.md", "conteudo do projeto")

	m := NewManager(fakeEmbedder{dim: 8}, "storage-key")
	m.SetSeedProject("realproj")
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 2 {
		t.Fatalf("want 2 seeded items (global + realproj), got %d", m.Len())
	}
}

func TestEnsureSeededIdempotent(t *testing.T) {
	root := t.TempDir()
	writeSeed(t, root, "global.md", "conteudo global")

	m := NewManager(fakeEmbedder{dim: 8}, "llm-bridge")
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 1 {
		t.Fatalf("want 1 item (not re-seeded), got %d", m.Len())
	}
}

// TestSeedLabelFor covers the seedLabelFor helper: a normal relative path
// yields the slash-relative label, and a pair that cannot be made relative
// (base relative vs target absolute) falls back to the basename.
func TestSeedLabelFor(t *testing.T) {
	if got := seedLabelFor("seeds", "seeds/sub/README.md"); got != "sub/README.md" {
		t.Fatalf("seedLabelFor(seeds, seeds/sub/README.md) = %q, want sub/README.md", got)
	}
	// filepath.Rel errors here (relative base vs absolute target) → basename fallback
	if got := seedLabelFor("seeds", "/abs/path/README.md"); got != "README.md" {
		t.Fatalf("seedLabelFor(seeds, /abs/...) = %q, want README.md (basename fallback)", got)
	}
}

func TestEnsureSeededSkipsUnreadable(t *testing.T) {
	root := t.TempDir()
	// A subdirectory of .md (not a file) should be skipped gracefully.
	writeSeed(t, root, "dir.md/note", "nested")

	m := NewManager(fakeEmbedder{dim: 8}, "llm-bridge")
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 0 {
		t.Fatalf("want 0 items, got %d", m.Len())
	}
}
