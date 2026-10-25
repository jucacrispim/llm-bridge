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
	"encoding/json"
	"strings"
	"testing"
)

// newTestSet returns a Set with a global and a project manager (fake
// embedders), optionally pre-populated with notes.
func newTestSet(t *testing.T) (*Set, *Manager, *Manager) {
	t.Helper()
	g := NewManager(fakeEmbedder{dim: 8}, GlobalKey)
	p := NewManager(fakeEmbedder{dim: 8}, "proj")
	s := NewSet(ScopeGlobal,
		Scope{Name: ScopeGlobal, M: g},
		Scope{Name: ScopeProject, M: p})
	return s, g, p
}

// TestSetScopesAndDefault covers the small accessors: the scope list keeps the
// configured order, Default reports the write target and a bogus default falls
// back to the first scope (a Set must always be writable).
func TestSetScopesAndDefault(t *testing.T) {
	s, _, _ := newTestSet(t)
	if got := strings.Join(s.Scopes(), ","); got != "global,project" {
		t.Fatalf("Scopes() = %q, want global,project", got)
	}
	if s.Default() != ScopeGlobal {
		t.Fatalf("Default() = %q, want global", s.Default())
	}

	// A default naming no scope falls back to the first one.
	other := NewSet("bogus", Scope{Name: ScopeProject, M: NewManager(fakeEmbedder{dim: 8}, "p")})
	if other.Default() != ScopeProject {
		t.Fatalf("Default() = %q, want project", other.Default())
	}
}

// TestSetShowGroupsByScope verifies that show lists both scopes with a header
// per scope, and that a single-scope Set renders exactly like Manager.Show.
func TestSetShowGroupsByScope(t *testing.T) {
	s, g, p := newTestSet(t)
	if _, err := g.Add("nota global", "texto global"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Add("nota projeto", "texto projeto"); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute(json.RawMessage(`{"command":"show"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[global]") || !strings.Contains(out, "[project]") {
		t.Fatalf("show = %q, want a header per scope", out)
	}
	if !strings.Contains(out, "nota global") || !strings.Contains(out, "nota projeto") {
		t.Fatalf("show = %q, want labels from both scopes", out)
	}

	// Single scope renders like Manager.Show (no headers).
	single := NewSet(ScopeProject, Scope{Name: ScopeProject, M: p})
	out, err = single.Execute(json.RawMessage(`{"command":"show"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "[project]") || !strings.Contains(out, "nota projeto") {
		t.Fatalf("single-scope show = %q", out)
	}
}

// TestSetSearchMergesScopes verifies that search covers every scope, that the
// merged hits are sorted by descending score and truncated to the limit, and
// that labels are prefixed with the scope name when there is more than one.
func TestSetSearchMergesScopes(t *testing.T) {
	s, g, p := newTestSet(t)
	if _, err := g.Add("nota global", "conteudo compartilhado"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Add("nota projeto", "conteudo compartilhado"); err != nil {
		t.Fatal(err)
	}
	out, err := s.Execute(json.RawMessage(`{"command":"search","query":"conteudo","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "global: nota global") || !strings.Contains(out, "project: nota projeto") {
		t.Fatalf("search = %q, want both scopes with prefixed labels", out)
	}

	// limit 1 keeps a single (best scored) hit out of the merged results.
	out, err = s.Execute(json.RawMessage(`{"command":"search","query":"conteudo","limit":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "(score "); n != 1 {
		t.Fatalf("limit=1 search returned %d hits: %q", n, out)
	}

	// A single-scope Set does not prefix labels.
	single := NewSet(ScopeProject, Scope{Name: ScopeProject, M: p})
	out, err = single.Execute(json.RawMessage(`{"command":"search","query":"conteudo"}`))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "project: ") {
		t.Fatalf("single-scope search should not prefix labels: %q", out)
	}
}

// TestSetWriteRouting verifies that add routes by the optional scope argument,
// defaulting to the global KB, and that delete/reset honor it too.
func TestSetWriteRouting(t *testing.T) {
	s, g, p := newTestSet(t)

	// Default: writes land in the global KB.
	out, err := s.Execute(json.RawMessage(`{"command":"add","label":"comum","text":"nota"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `(scope "global")`) {
		t.Fatalf("add result = %q, want the scope in the reply", out)
	}
	if g.Len() != 1 || p.Len() != 0 {
		t.Fatalf("default add: global=%d project=%d, want 1/0", g.Len(), p.Len())
	}

	// Explicit scope: the project KB.
	out, err = s.Execute(json.RawMessage(`{"command":"add","label":"local","text":"nota","scope":"project"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `(scope "project")`) {
		t.Fatalf("add result = %q, want the project scope", out)
	}
	if p.Len() != 1 {
		t.Fatalf("project add: project len = %d, want 1", p.Len())
	}

	// Delete honors the scope as well.
	if _, err := s.Execute(json.RawMessage(`{"command":"delete","label":"local","scope":"project"}`)); err != nil {
		t.Fatal(err)
	}
	if p.Len() != 0 {
		t.Fatalf("project delete: project len = %d, want 0", p.Len())
	}
	if g.Len() != 1 {
		t.Fatalf("project delete should not touch the global KB, len = %d", g.Len())
	}

	// Reset honors it too (wipes only the targeted KB).
	if _, err := s.Execute(json.RawMessage(`{"command":"reset","scope":"global"}`)); err != nil {
		t.Fatal(err)
	}
	if g.Len() != 0 {
		t.Fatalf("global reset: global len = %d, want 0", g.Len())
	}
}

// TestSetUnknownScope verifies that a scope naming a KB that is not in the set
// errors (listing the available ones) with two scopes, while a single-scope Set
// still writes to its only KB and reports the mismatch as a note.
func TestSetUnknownScope(t *testing.T) {
	s, g, _ := newTestSet(t)
	if _, err := s.Execute(json.RawMessage(`{"command":"add","label":"x","text":"y","scope":"bogus"}`)); err == nil {
		t.Fatal("want an error for an unknown scope with two active KBs")
	} else if !strings.Contains(err.Error(), "available: global, project") {
		t.Fatalf("error = %v, want it to list the available scopes", err)
	}

	single := NewSet(ScopeGlobal, Scope{Name: ScopeGlobal, M: g})
	out, err := single.Execute(json.RawMessage(`{"command":"add","label":"x","text":"y","scope":"project"}`))
	if err != nil {
		t.Fatalf("single-scope add should not error: %v", err)
	}
	if !strings.Contains(out, `scope "project" not active`) {
		t.Fatalf("single-scope add = %q, want an inactive-scope note", out)
	}
	if g.Len() != 1 {
		t.Fatalf("single-scope add should land in the global KB, len = %d", g.Len())
	}
}

// TestSetExecuteErrors covers the invalid invocations of a Set: unknown command,
// missing query and malformed JSON.
func TestSetExecuteErrors(t *testing.T) {
	s, _, _ := newTestSet(t)
	for _, c := range []string{
		`{"command":"bogus"}`,
		`{"command":"search"}`,
		`{"command":"add","label":"x"}`, // missing text (delegated to Manager)
		`not json`,
	} {
		if _, err := s.Execute(json.RawMessage(c)); err == nil {
			t.Errorf("Execute(%s) should error", c)
		}
	}
}

// TestSetLen verifies that Len sums the items of every scope.
func TestSetLen(t *testing.T) {
	s, g, p := newTestSet(t)
	if s.Len() != 0 {
		t.Fatalf("empty set Len() = %d, want 0", s.Len())
	}
	if _, err := g.Add("a", "texto a"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Add("b", "texto b"); err != nil {
		t.Fatal(err)
	}
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
}

// TestIsScope covers the flag/override validation helper.
func TestIsScope(t *testing.T) {
	for _, ok := range []string{ScopeGlobal, ScopeProject, ScopeBoth} {
		if !IsScope(ok) {
			t.Errorf("IsScope(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "geral", "GLOBAL"} {
		if IsScope(bad) {
			t.Errorf("IsScope(%q) = true, want false", bad)
		}
	}
}
