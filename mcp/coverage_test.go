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

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-bridge/sandbox"
)

// fakeCaged writes a caged stand-in that drops the policy flags and execs the
// command, so the sandboxed path of the client runs without a real cage.
func fakeCaged(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caged")
	script := "#!/bin/sh\n" +
		"while [ $# -gt 0 ] && [ \"$1\" != \"--\" ]; do shift; done\n" +
		"[ $# -gt 0 ] && shift\n" +
		"exec \"$@\"\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestDefaultPathAndLoad covers the config location: an empty Load path uses
// the user config dir, and a missing file is an empty config.
func TestDefaultPathAndLoad(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	want := filepath.Join(dir, "llm-bridge", "mcp.json")
	if got := DefaultPath(); got != want {
		t.Errorf("default path: got %q want %q", got, want)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("missing file gave servers: %v", cfg.Servers)
	}
}

// TestExpandPath covers the three shapes the configuration may use.
func TestExpandPath(t *testing.T) {
	t.Setenv("HOME", "/home/someone")
	if got := expandPath("~"); got != "/home/someone" {
		t.Errorf("bare tilde: got %q", got)
	}
	if got := expandPath("~/x"); got != "/home/someone/x" {
		t.Errorf("tilde path: got %q", got)
	}
	t.Setenv("HOME", "")
	if got := expandPath("~/x"); got != "~/x" {
		t.Errorf("no home dir: got %q", got)
	}
	if got := expandPath("/abs"); got != "/abs" {
		t.Errorf("absolute path: got %q", got)
	}
}

// TestEmptyManager checks the zero manager: no tools, nothing known.
func TestEmptyManager(t *testing.T) {
	m := Empty()
	if len(m.Tools()) != 0 || m.Has("anything") {
		t.Error("Empty() should know nothing")
	}
	if !m.Mutates("anything") {
		t.Error("an unknown tool must be treated as mutating")
	}
	if _, err := m.Call(context.Background(), "anything", nil, "", nil); err == nil {
		t.Fatal("expected an error for an unknown tool")
	}
	var nilManager *Manager
	if nilManager.Tools() != nil || nilManager.Has("x") {
		t.Error("a nil manager should behave like an empty one")
	}
}

// TestManagerSkipsDuplicateTool: the same exposed name twice would be two
// different calls behind one name, so the first one wins.
func TestManagerSkipsDuplicateTool(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"dup"},{"name":"dup"}]}\n' ;;
  esac
done`)}
	m := NewManager([]*Server{srv})
	if len(m.Tools()) != 1 {
		t.Fatalf("tools: %+v", m.Tools())
	}
}

// TestServerListWithoutTools: an answer without a tools list is a failure to
// list, not an empty server.
func TestServerListWithoutTools(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `printf '{"other":1}\n'`)}
	if _, err := srv.List(context.Background()); err == nil {
		t.Fatal("expected an error for an answer without tools")
	}
}

// TestServerListSkipsNamelessTool: a tool without a name cannot be called, so
// it never reaches the model.
func TestServerListSkipsNamelessTool(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `printf '{"tools":[{"name":""},{"name":"a"}]}\n'`)}
	tools, err := srv.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "a" {
		t.Errorf("tools: %+v", tools)
	}
}

// TestServerCallAcceptsLooseShapes: a value may be a bare JSON string, an
// array, or an object with an empty text part. None of them is a protocol
// error.
func TestServerCallAcceptsLooseShapes(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `printf '"plain"\n'
printf '[1,2]\n'
printf '{"content":[{"type":"text","text":""}],"text":"ok"}\n'`)}
	var got strings.Builder
	if err := srv.Call(context.Background(), "x", nil, "", func(s string) error {
		got.WriteString(s)
		return nil
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	// The empty text part adds nothing; the other two shapes keep their text.
	if got.String() != "plain[1,2]ok" {
		t.Errorf("text: %q", got.String())
	}
}

// TestServerUnderSandbox: the configured server runs through the sandbox, not
// only directly.
func TestServerUnderSandbox(t *testing.T) {
	sb, err := sandbox.New(fakeCaged(t), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Name: "fake", Bin: fakeServer(t, echoServer), Sandbox: sb}
	tools, err := srv.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("tools: %+v", tools)
	}
}

// TestServerDir: the configured working directory is where the server starts,
// with or without a sandbox.
func TestServerDir(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	bin := fakeServer(t, `printf '{"tools":[{"name":"%s"}]}\n' "$(pwd -P)"`)
	srv := &Server{Name: "fake", Bin: bin, Dir: dir}
	tools, err := srv.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != real {
		t.Errorf("tools: %+v want name %q", tools, real)
	}
}

// TestServerCallDir: a call runs where the prompt is, and a cwd written in the
// config still wins over it.
func TestServerCallDir(t *testing.T) {
	promptDir := t.TempDir()
	configDir := t.TempDir()
	realPrompt, err := filepath.EvalSymlinks(promptDir)
	if err != nil {
		t.Fatal(err)
	}
	realConfig, err := filepath.EvalSymlinks(configDir)
	if err != nil {
		t.Fatal(err)
	}
	bin := fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/call*) printf '{"text":"%s"}\n' "$(pwd -P)" ;;
  esac
done`)
	call := func(srv *Server, dir string) string {
		t.Helper()
		var got strings.Builder
		if err := srv.Call(context.Background(), "x", nil, dir, func(s string) error {
			got.WriteString(s)
			return nil
		}); err != nil {
			t.Fatalf("call: %v", err)
		}
		return got.String()
	}

	srv := &Server{Name: "fake", Bin: bin}
	if got := call(srv, promptDir); got != realPrompt {
		t.Errorf("without a config cwd: got %q want %q", got, realPrompt)
	}
	srv.Dir = configDir
	if got := call(srv, promptDir); got != realConfig {
		t.Errorf("config cwd should win: got %q want %q", got, realConfig)
	}
}

// TestManagerCallStopsOnTotal: a server that keeps the call open past the total
// deadline is cut, and an empty result still says why.
func TestManagerCallStopsOnTotal(t *testing.T) {
	srv := &Server{
		Name: "slow",
		Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) sleep 30 ;;
  esac
done`),
		Limits: sandbox.StreamLimits{Idle: 30 * time.Second, Total: 200 * time.Millisecond},
	}
	m := NewManager([]*Server{srv})
	out, err := m.Call(context.Background(), "mcp__slow__x", nil, "", nil)
	if err != nil {
		t.Fatalf("a stopped stream is not a failure: %v", err)
	}
	if !strings.Contains(out, "total deadline") {
		t.Errorf("result: %q", out)
	}
}

// TestManagerCallStopsOnBytes: output past the cap is cut with a note, and an
// empty result still says why.
func TestManagerCallStopsOnBytes(t *testing.T) {
	srv := &Server{
		Name: "loud",
		Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) printf '{"text":"0123456789012345678901234567890123456789"}\n' ;;
  esac
done`),
		// Enough for the listing, not for the call.
		Limits: sandbox.StreamLimits{MaxBytes: 40},
	}
	m := NewManager([]*Server{srv})
	out, err := m.Call(context.Background(), "mcp__loud__x", nil, "", nil)
	if err != nil {
		t.Fatalf("a stopped stream is not a failure: %v", err)
	}
	if !strings.Contains(out, "byte limit") {
		t.Errorf("result: %q", out)
	}
}

// TestManagerCallToolError: a call the server marks as failed becomes an error
// result for the model, with a message even when the server sent no text.
func TestManagerCallToolError(t *testing.T) {
	withText := &Server{Name: "bad", Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) printf '{"isError":true,"text":"boom"}\n' ;;
  esac
done`)}
	m := NewManager([]*Server{withText})
	out, err := m.Call(context.Background(), "mcp__bad__x", nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if out != "error: boom" {
		t.Errorf("result: %q", out)
	}

	silent := &Server{Name: "bad", Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) printf '{"isError":true}\n' ;;
  esac
done`)}
	m = NewManager([]*Server{silent})
	out, err = m.Call(context.Background(), "mcp__bad__x", nil, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reported an error") {
		t.Errorf("result: %q", out)
	}
}

// TestManagerCallRejectsBadArguments: a payload that is not JSON is the
// model's mistake, reported as such instead of being sent to the server.
func TestManagerCallRejectsBadArguments(t *testing.T) {
	m := NewManager([]*Server{{Name: "fake", Bin: fakeServer(t, echoServer)}})
	if _, err := m.Call(context.Background(), "mcp__fake__echo", json.RawMessage("{bad"), "", nil); err == nil {
		t.Fatal("expected an error for malformed arguments")
	}
}

// TestServerListUnreadableAnswer: a value that is valid JSON but not an object
// cannot be a tools/list answer.
func TestServerListUnreadableAnswer(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `printf '"nope"\n'`)}
	if _, err := srv.List(context.Background()); err == nil {
		t.Fatal("expected an error for a non-object answer")
	}
}

// TestServerCallConsumerError: a consumer that gives up ends the call, and its
// error is what the caller sees.
func TestServerCallConsumerError(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `printf '{"content":[{"type":"text","text":"x"}]}\n'`)}
	stop := errors.New("enough")
	err := srv.Call(context.Background(), "x", nil, "", func(string) error { return stop })
	if !errors.Is(err, stop) {
		t.Fatalf("expected the consumer error, got %v", err)
	}
}

// TestConfigWithoutConfigDir: a machine without $XDG_CONFIG_HOME or $HOME has
// no default path, and Load treats that as no servers.
func TestConfigWithoutConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := DefaultPath(); got != "" {
		t.Errorf("default path without a config dir: %q", got)
	}
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Servers) != 0 {
		t.Errorf("servers: %v", cfg.Servers)
	}
}

// TestLoadErrors covers the two ways the file itself is bad: unreadable and
// not JSON.
func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(dir); err == nil {
		t.Error("expected an error for a directory")
	}
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}

// TestLimitsRejectsBadTotal: a typo in the total duration is caught with the
// same care as the idle one.
func TestLimitsRejectsBadTotal(t *testing.T) {
	if _, err := (ServerConfig{Total: "later"}).Limits(); err == nil {
		t.Fatal("expected an error for an unparsable total")
	}
}

// TestConfigWritableGit: a git server asks for a writable .git, and the field
// has to survive the trip from the file, where the sandbox reads it.
func TestConfigWritableGit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{"servers":{"git":{"command":"extra-tools","writable_git":true},
		"plain":{"command":"extra-tools"}}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Servers["git"].WritableGit {
		t.Error("writable_git was not read")
	}
	if cfg.Servers["plain"].WritableGit {
		t.Error("writable_git defaults to false")
	}
}
