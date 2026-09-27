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

package hooks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeHook creates a hooks script in dir/.llm-bridge/hooks/<name>.sh and
// returns its path.
func writeHook(t *testing.T, dir, name, body string) string {
	t.Helper()
	hd := filepath.Join(dir, ".llm-bridge", "hooks")
	if err := os.MkdirAll(hd, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(hd, name+".sh")
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLookupProject(t *testing.T) {
	dir := t.TempDir()
	want := writeHook(t, dir, "algo", "#!/bin/sh\necho hi\n")
	got, err := Lookup(dir, "algo")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestLookupNotFound(t *testing.T) {
	dir := t.TempDir()
	if _, err := Lookup(dir, "nope"); err == nil {
		t.Fatal("expected error for missing hook")
	}
}

func TestLookupInvalidName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "../x", "a/b", "a b", "a\\b"} {
		if _, err := Lookup(dir, name); err == nil {
			t.Errorf("expected error for invalid name %q", name)
		}
	}
}

func TestRunOutputAndArgs(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "echo", "#!/bin/sh\nprintf 'seen:%s ' \"$@\"\n")
	out, err := Run(dir, "echo", []string{"a", "b"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out, "a") || !strings.Contains(out, "b") {
		t.Errorf("args not passed through: %q", out)
	}
}

func TestRunMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := Run(dir, "nope", nil); err == nil {
		t.Fatal("expected error for missing hook")
	}
}

func TestRunCwd(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "pwd", "#!/bin/sh\npwd\n")
	out, err := Run(dir, "pwd", nil)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(out) != dir {
		t.Errorf("hook ran in %q, want cwd %q", strings.TrimSpace(out), dir)
	}
}
