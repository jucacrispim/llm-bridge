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
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-bridge/sandbox"
)

// fakeCaged writes a wrapper script that stands in for caged: it skips the
// policy flags up to "--" and runs body with the command in "$@".
func fakeCaged(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "caged")
	script := "#!/bin/sh\nwhile [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\n[ $# -gt 0 ] && shift\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestRunContextSandboxed checks a hook still runs (through caged) and that the
// sandbox policy is used as given (the paths come from the bridge's own
// -sandbox-ro/-sandbox-rw, never from the hook's cwd).
func TestRunContextSandboxed(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "pwd", "#!/bin/sh\npwd\n")
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := RunContext(context.Background(), dir, "pwd", nil, sb)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.TrimSpace(out) != dir {
		t.Errorf("hook ran in %q, want %q", strings.TrimSpace(out), dir)
	}
	if strings.Contains(strings.Join(sb.Policy.ReadWrite, ":"), dir) {
		t.Errorf("hook cwd should not be granted by the hook itself: %v", sb.Policy.ReadWrite)
	}
}

// TestRunContextSandboxSetupFailure checks a caged failure (reported on its
// report descriptor) is not reported as the hook's own exit status.
func TestRunContextSandboxSetupFailure(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "noop", "#!/bin/sh\nexit 0\n")
	sb, err := sandbox.New(fakeCaged(t, "printf 'error restricting self\\n' >&3; exit 125"), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunContext(context.Background(), dir, "noop", nil, sb)
	if err == nil {
		t.Fatal("expected an error for a failed sandbox")
	}
	var se *sandbox.SetupError
	if !errors.As(err, &se) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(se.Output, "error restricting self") {
		t.Errorf("unexpected output: %q", se.Output)
	}
}

// TestRunContextCommandStatus checks the other side: a script exiting 125 is
// the script's own failure, since the report descriptor stays empty.
func TestRunContextCommandStatus(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "boom", "#!/bin/sh\nexit 125\n")
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = RunContext(context.Background(), dir, "boom", nil, sb)
	if err == nil {
		t.Fatal("expected the script's non-zero status to be an error")
	}
	var se *sandbox.SetupError
	if errors.As(err, &se) {
		t.Fatalf("the script's status was taken for a sandbox failure: %v", se)
	}
}

// TestRunContextCancel checks the hook is killed when the context is cancelled
// (Run, which uses a background context, keeps the old behavior).
func TestRunContextCancel(t *testing.T) {
	dir := t.TempDir()
	writeHook(t, dir, "slow", "#!/bin/sh\nsleep 30\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunContext(ctx, dir, "slow", nil, nil); err == nil {
		t.Fatal("expected the cancelled hook to fail")
	}
}
