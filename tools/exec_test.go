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
)

func writeExecFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestExecuteDispatchRead covers the Executor.Execute entry point for the read
// tool (a valid call and a malformed-args call, which is tolerated and surfaces
// the individual tool's own "missing" error).
func TestExecuteDispatchRead(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "hello")

	e := NewExecutor(dir)
	out, err := e.Execute(context.Background(), "read", `{"path":"a.txt"}`)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if out != "hello" {
		t.Fatalf("read output = %q, want hello", out)
	}

	// Malformed arguments are tolerated: the read tool reports its own error.
	if _, err := e.Execute(context.Background(), "read", "not json"); err == nil ||
		!strings.Contains(err.Error(), "missing 'path'") {
		t.Fatalf("malformed args: err = %v, want missing 'path'", err)
	}
}

// TestExecuteDispatchGrep covers the grep branch of Execute.
func TestExecuteDispatchGrep(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "hello world\n")

	e := NewExecutor(dir)
	out, err := e.Execute(context.Background(), "grep", `{"pattern":"hello"}`)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !strings.Contains(out, "hello world") {
		t.Fatalf("grep output = %q, want a match on 'hello world'", out)
	}
}

// TestExecuteUnknownTool covers the default branch: an unknown tool name is an
// error.
func TestExecuteUnknownTool(t *testing.T) {
	e := NewExecutor("")
	_, err := e.Execute(context.Background(), "nope", "{}")
	if err == nil || !strings.Contains(err.Error(), "unknown tool: nope") {
		t.Fatalf("unknown tool err = %v, want unknown tool: nope", err)
	}
}

// TestResolve covers the relative/absolute/empty path resolution against cwd.
func TestResolve(t *testing.T) {
	e := NewExecutor("/base")
	if got := e.resolve(""); got != "" {
		t.Errorf("resolve(\"\") = %q, want empty", got)
	}
	if got := e.resolve("rel"); got != filepath.Join("/base", "rel") {
		t.Errorf("resolve(\"rel\") = %q, want /base/rel", got)
	}
	if got := e.resolve("/abs"); got != "/abs" {
		t.Errorf("resolve(\"/abs\") = %q, want /abs", got)
	}
	// with no cwd, relative paths are left as-is (the process cwd resolves them)
	e2 := NewExecutor("")
	if got := e2.resolve("rel"); got != "rel" {
		t.Errorf("resolve with empty cwd = %q, want rel", got)
	}
}

// TestExpandHome covers the leading-~ expansion and the pass-through cases.
func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	if got := expandHome("~"); got != home {
		t.Errorf("expandHome(\"~\") = %q, want %q", got, home)
	}
	if got := expandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("expandHome(\"~/x\") = %q, want %q", got, filepath.Join(home, "x"))
	}
	if got := expandHome("/abs"); got != "/abs" {
		t.Errorf("expandHome(\"/abs\") = %q, want /abs", got)
	}
	if got := expandHome("rel"); got != "rel" {
		t.Errorf("expandHome(\"rel\") = %q, want rel", got)
	}

	// When the home dir cannot be resolved (HOME unset), "~" is left as-is.
	t.Setenv("HOME", "")
	if got := expandHome("~"); got != "~" {
		t.Errorf("expandHome(\"~\") without HOME = %q, want ~", got)
	}
	if got := expandHome("~/x"); got != "~/x" {
		t.Errorf("expandHome(\"~/x\") without HOME = %q, want ~/x", got)
	}
}

// TestNumber covers the numeric conversion helper for every supported JSON
// number type plus a non-numeric value (treated as absent).
func TestNumber(t *testing.T) {
	if n, ok := number(float64(5)); !ok || n != 5 {
		t.Errorf("number(float64 5) = %d, %v; want 5, true", n, ok)
	}
	if n, ok := number(3); !ok || n != 3 {
		t.Errorf("number(int 3) = %d, %v; want 3, true", n, ok)
	}
	if n, ok := number(int64(7)); !ok || n != 7 {
		t.Errorf("number(int64 7) = %d, %v; want 7, true", n, ok)
	}
	if _, ok := number("x"); ok {
		t.Errorf("number(\"x\") reported present, want absent")
	}
}

// TestReadWholeFile covers read without offset/limit.
func TestReadWholeFile(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "l1\nl2\nl3")

	e := NewExecutor(dir)
	out, err := e.read(map[string]any{"path": "a.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "l1\nl2\nl3" {
		t.Fatalf("read = %q, want full content", out)
	}
}

// TestReadOffsetLimit covers every branch of the offset/limit slicing,
// including the clamping of negative and out-of-range values.
func TestReadOffsetLimit(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "l1\nl2\nl3\nl4")
	e := NewExecutor(dir)

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"offset only", map[string]any{"path": "a.txt", "offset": 2}, "l3\nl4"},
		{"offset+limit", map[string]any{"path": "a.txt", "offset": 2, "limit": 1}, "l3"},
		{"negative offset clamps to 0", map[string]any{"path": "a.txt", "offset": -1, "limit": 1}, "l1"},
		{"offset past end", map[string]any{"path": "a.txt", "offset": 100, "limit": 1}, ""},
		{"limit past end", map[string]any{"path": "a.txt", "offset": 0, "limit": 100}, "l1\nl2\nl3\nl4"},
		{"negative limit yields empty", map[string]any{"path": "a.txt", "offset": 1, "limit": -1}, ""},
	}
	for _, tc := range cases {
		out, err := e.read(tc.args)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if out != tc.want {
			t.Errorf("%s: read = %q, want %q", tc.name, out, tc.want)
		}
	}
}

// TestReadMissingPath covers the missing-path guard.
func TestReadMissingPath(t *testing.T) {
	e := NewExecutor("")
	if _, err := e.read(map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "missing 'path'") {
		t.Fatalf("read missing path err = %v, want missing 'path'", err)
	}
}

// TestWrite covers a successful write and the missing-arguments guard.
func TestWrite(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(dir)
	out, err := e.write(map[string]any{"path": "sub/a.txt", "content": "data"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("write = %q, want ok", out)
	}
	got, err := os.ReadFile(filepath.Join(dir, "sub", "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "data" {
		t.Fatalf("file content = %q, want data", got)
	}

	if _, err := e.write(map[string]any{"path": "x"}); err == nil ||
		!strings.Contains(err.Error(), "missing 'path' or 'content'") {
		t.Fatalf("write missing args err = %v, want missing 'path' or 'content'", err)
	}
}

// TestShell covers a successful command, the missing-command guard, the cwd
// application, and a non-zero exit status.
func TestShell(t *testing.T) {
	dir := t.TempDir()
	e := NewExecutor(dir)

	out, err := e.shell(context.Background(), map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "hi" {
		t.Fatalf("shell output = %q, want hi", out)
	}

	// cwd is applied: pwd must report the executor's cwd.
	out, err = e.shell(context.Background(), map[string]any{"command": "pwd"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(out)
	if r, err := filepath.EvalSymlinks(got); err == nil {
		got = r
	}
	want := dir
	if r, err := filepath.EvalSymlinks(want); err == nil {
		want = r
	}
	if got != want {
		t.Fatalf("shell pwd = %q, want %q", got, want)
	}

	// a non-zero exit appends the status line and is not an error.
	out, err = e.shell(context.Background(), map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[exited with status 3]") {
		t.Fatalf("shell exit output = %q, want exited-with-status marker", out)
	}

	if _, err := e.shell(context.Background(), map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "missing 'command'") {
		t.Fatalf("shell missing command err = %v, want missing 'command'", err)
	}
}

// TestGrep covers grep with a match, without a match (exit 1 is not an error),
// the default "." path, and the missing-pattern guard.
func TestGrep(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "needle here\n")

	e := NewExecutor(dir)
	out, err := e.grep(context.Background(), map[string]any{"pattern": "needle", "path": "."})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needle here") {
		t.Fatalf("grep output = %q, want a match", out)
	}

	// default path "." when unset
	out, err = e.grep(context.Background(), map[string]any{"pattern": "needle"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "needle here") {
		t.Fatalf("grep default path output = %q, want a match", out)
	}

	// no match → empty output, no error
	out, err = e.grep(context.Background(), map[string]any{"pattern": "zzz-no-match"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("grep no-match output = %q, want empty", out)
	}

	if _, err := e.grep(context.Background(), map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "missing 'pattern'") {
		t.Fatalf("grep missing pattern err = %v, want missing 'pattern'", err)
	}
}

// TestGlob covers glob with an explicit base path, the default base (cwd), no
// matches, and the missing-pattern guard.
func TestGlob(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "a.txt"), "a")
	writeExecFile(t, filepath.Join(dir, "b.txt"), "b")
	writeExecFile(t, filepath.Join(dir, "c.go"), "c")

	e := NewExecutor(dir)
	out, err := e.glob(map[string]any{"pattern": "*.txt", "path": dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") || strings.Contains(out, "c.go") {
		t.Fatalf("glob output = %q, want a.txt and b.txt (relative)", out)
	}

	// default base = cwd
	out, err = e.glob(map[string]any{"pattern": "*.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "a.txt") {
		t.Fatalf("glob default-base output = %q, want a.txt", out)
	}

	out, err = e.glob(map[string]any{"pattern": "*.nope"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "" {
		t.Fatalf("glob no-match output = %q, want empty", out)
	}

	if _, err := e.glob(map[string]any{}); err == nil ||
		!strings.Contains(err.Error(), "missing 'pattern'") {
		t.Fatalf("glob missing pattern err = %v, want missing 'pattern'", err)
	}
}

// TestSearchReplace covers a successful literal replacement, the missing-args
// guard, and the not-found error.
func TestSearchReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.txt")
	writeExecFile(t, path, "hello world")

	e := NewExecutor(dir)
	out, err := e.searchReplace(map[string]any{"path": "a.txt", "search": "world", "replace": "there"})
	if err != nil {
		t.Fatal(err)
	}
	if out != "ok" {
		t.Fatalf("searchReplace = %q, want ok", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello there" {
		t.Fatalf("file content = %q, want 'hello there'", got)
	}

	if _, err := e.searchReplace(map[string]any{"path": "a.txt"}); err == nil ||
		!strings.Contains(err.Error(), "missing 'path', 'search' or 'replace'") {
		t.Fatalf("searchReplace missing args err = %v, want missing-args error", err)
	}

	if _, err := e.searchReplace(map[string]any{"path": "a.txt", "search": "nope", "replace": "x"}); err == nil ||
		!strings.Contains(err.Error(), "not found") {
		t.Fatalf("searchReplace not-found err = %v, want not-found error", err)
	}
}
