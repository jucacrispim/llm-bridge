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

func TestAllReturnsEightTools(t *testing.T) {
	got := All()
	if len(got) != 8 {
		t.Fatalf("len(All()) = %d, want 8", len(got))
	}
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name] = true
	}
	expected := []string{"read", "write", "shell", "grep", "glob", "curl", "search_replace", "knowledge"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}

// TestIsReadOnly verifies the read-only classification: read-only tools report
// true, mutating tools report false, and an unknown tool is treated as mutating.
func TestIsReadOnly(t *testing.T) {
	for _, name := range []string{"read", "grep", "glob"} {
		if !IsReadOnly(name) {
			t.Errorf("IsReadOnly(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"write", "shell", "search_replace", "curl", "knowledge"} {
		if IsReadOnly(name) {
			t.Errorf("IsReadOnly(%q) = true, want false", name)
		}
	}
	if IsReadOnly("does-not-exist") {
		t.Errorf("IsReadOnly(unknown) = true, want false")
	}
}

// TestMutatesCallIsPerKnowledgeCommand pins the per-call classification: the
// tool is statically mutating, but a lookup must not wait on a human. If this
// ever flips, every session becomes a click-fest; if a write stops being
// mutating, an injected page can plant a note with nobody watching.
func TestMutatesCallIsPerKnowledgeCommand(t *testing.T) {
	for _, cmd := range []string{"add", "delete", "reset"} {
		if !MutatesCall("knowledge", `{"command":"`+cmd+`"}`) {
			t.Errorf("knowledge %s must be mutating", cmd)
		}
	}
	for _, cmd := range []string{"show", "search", " SHOW "} {
		if MutatesCall("knowledge", `{"command":"`+cmd+`"}`) {
			t.Errorf("knowledge %q is a read and must not ask for confirmation", cmd)
		}
	}
	// Anything we cannot classify is confirmed rather than executed.
	if !MutatesCall("knowledge", `not json`) {
		t.Error("a malformed knowledge call must be treated as mutating")
	}
	if !MutatesCall("knowledge", `{"command":"bogus"}`) {
		t.Error("an unknown knowledge command must be treated as mutating")
	}
	// Every other tool keeps its static classification.
	if MutatesCall("read", `{"path":"x"}`) || MutatesCall("glob", `{"pattern":"*"}`) {
		t.Error("read/glob are reads")
	}
	if !MutatesCall("shell", `{"command":"true"}`) || !MutatesCall("write", `{"path":"x"}`) {
		t.Error("shell/write mutate")
	}
}
