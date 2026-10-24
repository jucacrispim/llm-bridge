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

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-bridge/sandbox"
)

// fakeCaged writes a wrapper script that stands in for caged, discards the
// policy flags up to "--" and then runs body with the command in "$@".
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

// TestExecuteSandboxed covers the happy path: with a sandbox configured the
// tool still runs (through caged) and returns the output.
func TestExecuteSandboxed(t *testing.T) {
	dir := t.TempDir()
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandbox(dir, sb)

	out, err := e.Execute(context.Background(), "shell", `{"command":"printf sandboxed"}`)
	if err != nil {
		t.Fatalf("shell: %v", err)
	}
	if out != "sandboxed" {
		t.Errorf("output: got %q", out)
	}

	writeExecFile(t, filepath.Join(dir, "g.txt"), "needle\n")
	out, err = e.Execute(context.Background(), "grep", `{"pattern":"needle","path":"."}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "needle") {
		t.Errorf("grep output: %q", out)
	}
}

// TestExecuteSandboxSetupFailure covers the failures that make the command not
// run at all: caged reports them on its report descriptor, and they must
// surface as errors, never as the tool's output.
func TestExecuteSandboxSetupFailure(t *testing.T) {
	for _, code := range []int{2, 125} {
		sb, err := sandbox.New(fakeCaged(t, "printf 'error allowing read access\\n' >&3; exit "+itoa(code)), sandbox.Policy{})
		if err != nil {
			t.Fatal(err)
		}
		e := NewExecutorWithSandbox(t.TempDir(), sb)

		out, err := e.Execute(context.Background(), "shell", `{"command":"true"}`)
		if err == nil {
			t.Fatalf("exit %d: expected an error, got output %q", code, out)
		}
		if !strings.Contains(err.Error(), "sandbox:") {
			t.Errorf("exit %d: unexpected error %q", code, err)
		}

		out, err = e.Execute(context.Background(), "grep", `{"pattern":"x"}`)
		if err == nil {
			t.Fatalf("exit %d: grep: expected an error, got output %q", code, out)
		}
		if !strings.Contains(err.Error(), "sandbox:") {
			t.Errorf("exit %d: grep: unexpected error %q", code, err)
		}
	}
}

// TestExecuteSandboxCommandStatus covers the other side of the report: a tool
// that legitimately exits 2 (an `ls` on a missing file, say) or 125 is the
// command's own result, not a broken sandbox.
func TestExecuteSandboxCommandStatus(t *testing.T) {
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandbox(t.TempDir(), sb)

	out, err := e.Execute(context.Background(), "shell", `{"command":"exit 2"}`)
	if err != nil {
		t.Fatalf("exit 2: unexpected error %v", err)
	}
	if !strings.Contains(out, "[exited with status 2]") {
		t.Errorf("exit 2: output %q", out)
	}

	// grep: "no matches" (exit 1) is a normal result, not a setup failure.
	if _, err := e.Execute(context.Background(), "grep", `{"pattern":"x"}`); err != nil {
		t.Fatalf("grep exit 1: unexpected error %v", err)
	}
}

// TestExecuteSandboxUsesPolicyAsGiven checks the executor does not touch the
// policy: the tool works on the same filesystem the bridge does, so the paths
// come from the bridge's own policy (-sandbox-ro/-sandbox-rw), never from the
// per-command cwd.
func TestExecuteSandboxUsesPolicyAsGiven(t *testing.T) {
	dir := t.TempDir()
	base := sandbox.Policy{ReadOnly: []string{"/usr", "/bin"}, ReadWrite: []string{"/dev/null"}}
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), base)
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandbox(dir, sb)
	if e.sandbox != sb {
		t.Fatal("executor did not keep the sandbox it was given")
	}
	if strings.Join(e.sandbox.Policy.ReadWrite, ":") != strings.Join(base.ReadWrite, ":") {
		t.Errorf("policy changed: got %v want %v", e.sandbox.Policy.ReadWrite, base.ReadWrite)
	}
	if strings.Contains(strings.Join(e.sandbox.Policy.ReadWrite, ":"), dir) {
		t.Errorf("cwd %q should not be granted by the executor: %v", dir, e.sandbox.Policy.ReadWrite)
	}
}

// itoa avoids importing strconv in the test only.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := ""
	for n > 0 {
		digits = string(rune('0'+n%10)) + digits
		n /= 10
	}
	return digits
}
