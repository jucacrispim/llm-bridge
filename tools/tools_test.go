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

package tools

import "testing"

func TestAllReturnsSevenTools(t *testing.T) {
	got := All()
	if len(got) != 7 {
		t.Fatalf("len(All()) = %d, want 7", len(got))
	}
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name] = true
	}
	expected := []string{"read", "write", "shell", "grep", "glob", "search_replace", "knowledge"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

// TestIsReadOnly verifies the read-only classification: read-only tools report
// true, mutating tools report false, and an unknown tool is treated as mutating.
func TestIsReadOnly(t *testing.T) {
	for _, name := range []string{"read", "grep", "glob", "knowledge"} {
		if !IsReadOnly(name) {
			t.Errorf("IsReadOnly(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"write", "shell", "search_replace"} {
		if IsReadOnly(name) {
			t.Errorf("IsReadOnly(%q) = true, want false", name)
		}
	}
	if IsReadOnly("does-not-exist") {
		t.Errorf("IsReadOnly(unknown) = true, want false")
	}
}
