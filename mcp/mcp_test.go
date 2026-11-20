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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"llm-bridge/sandbox"
)

// fakeServer writes a script that answers tools/list and tools/call, so the
// client is exercised over a real pipe without a real MCP server.
func fakeServer(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const echoServer = `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"echo","description":"Echo it","inputSchema":{"type":"object"}},{"name":"second"}]}\n' ;;
    *tools/call*) printf '{"text":"one"}\n{"content":[{"type":"text","text":"two"}]}\n' ;;
  esac
done`

func TestExposedName(t *testing.T) {
	if got := exposedName("tupi", "search"); got != "mcp__tupi__search" {
		t.Errorf("name: got %q", got)
	}
	if got := exposedName("a.b", "x/y"); got != "mcp__a_b__x_y" {
		t.Errorf("sanitized name: got %q", got)
	}
	long := exposedName(strings.Repeat("s", 60), strings.Repeat("t", 60))
	if len(long) > maxToolName {
		t.Errorf("name too long: %d chars", len(long))
	}
	other := exposedName(strings.Repeat("s", 60), strings.Repeat("t", 59)+"u")
	if long == other {
		t.Error("two long names truncated to the same string")
	}
}

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	content := `{"servers":{"tupi":{"command":"~/bin/tupi-tools","ro":["~/data"],
		"read_only":["search"],"idle":"5s","total":"1m"},"net":{"command":"srv","network":true,"ports":[8443]}}}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	names := cfg.Names()
	if len(names) != 2 || names[0] != "net" || names[1] != "tupi" {
		t.Fatalf("names: %v", names)
	}
	tupi := cfg.Servers["tupi"].Normalize()
	home, _ := os.UserHomeDir()
	if !strings.HasPrefix(tupi.Command, home) {
		t.Errorf("command not expanded: %q", tupi.Command)
	}
	if !strings.HasPrefix(tupi.RO[0], home) {
		t.Errorf("ro not expanded: %q", tupi.RO)
	}
	limits, err := tupi.Limits()
	if err != nil {
		t.Fatal(err)
	}
	if limits.Idle != 5*time.Second || limits.Total != time.Minute {
		t.Errorf("limits: %+v", limits)
	}
	if !tupi.ReadOnlyTool("search") || tupi.ReadOnlyTool("write") {
		t.Error("read_only list not honored")
	}

	missing, err := Load(filepath.Join(dir, "absent.json"))
	if err != nil || len(missing.Servers) != 0 {
		t.Errorf("missing file: cfg=%v err=%v", missing, err)
	}
}

func TestLoadConfigRejectsBadEntry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte(`{"servers":{"a b":{"command":"x"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for an invalid server name")
	}
	if err := os.WriteFile(path, []byte(`{"servers":{"srv":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected an error for a missing command")
	}
	if err := os.WriteFile(path, []byte(`{"servers":{"srv":{"command":"x","idle":"soon"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Servers["srv"].Limits(); err == nil {
		t.Fatal("expected an error for an unparsable idle")
	}
}

func TestServerListAndCall(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, echoServer)}
	ctx := context.Background()
	tools, err := srv.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "echo" || tools[1].Name != "second" {
		t.Fatalf("tools: %+v", tools)
	}
	if string(tools[0].Parameters) != `{"type":"object"}` {
		t.Errorf("schema: %s", tools[0].Parameters)
	}

	var fragments []string
	if err := srv.Call(ctx, "echo", json.RawMessage(`{"q":"x"}`), "", func(s string) error {
		fragments = append(fragments, s)
		return nil
	}); err != nil {
		t.Fatalf("call: %v", err)
	}
	if len(fragments) != 2 || fragments[0] != "one" || fragments[1] != "two" {
		t.Errorf("fragments: %v", fragments)
	}
}

func TestServerCallToolError(t *testing.T) {
	srv := &Server{Name: "fake", Bin: fakeServer(t, `while IFS= read -r line; do
  printf '{"isError":true,"text":"boom"}\n'
done`)}
	var got string
	err := srv.Call(context.Background(), "x", nil, "", func(s string) error {
		got += s
		return nil
	})
	if err != ErrToolError {
		t.Fatalf("expected ErrToolError, got %v", err)
	}
	if got != "boom" {
		t.Errorf("text: %q", got)
	}
}

func TestNewManagerToolsAndClassification(t *testing.T) {
	servers := []*Server{
		{Name: "fake", Bin: fakeServer(t, echoServer), ReadOnly: map[string]bool{"echo": true}},
		{Name: "dead", Bin: filepath.Join(t.TempDir(), "missing")},
	}
	m := NewManager(servers)
	tools := m.Tools()
	if len(tools) != 2 {
		t.Fatalf("tools: %+v", tools)
	}
	if tools[0].Name != "mcp__fake__echo" || tools[1].Name != "mcp__fake__second" {
		t.Errorf("names: %s %s", tools[0].Name, tools[1].Name)
	}
	if !m.Has("mcp__fake__echo") || m.Has("mcp__fake__absent") {
		t.Error("Has is wrong")
	}
	if m.Mutates("mcp__fake__echo") {
		t.Error("echo is listed as read-only in the config")
	}
	if !m.Mutates("mcp__fake__second") {
		t.Error("second has no read_only entry, so it mutates")
	}
	if m.Has("mcp__dead__anything") {
		t.Error("a server that failed to list kept its tools")
	}
}

func TestManagerCallJoinsFragments(t *testing.T) {
	m := NewManager([]*Server{{Name: "fake", Bin: fakeServer(t, echoServer)}})
	var streamed []string
	out, err := m.Call(context.Background(), "mcp__fake__echo", nil, "", func(s string) error {
		streamed = append(streamed, s)
		return nil
	})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if out != "onetwo" {
		t.Errorf("result: %q", out)
	}
	if len(streamed) != 2 {
		t.Errorf("streamed fragments: %v", streamed)
	}
}

func TestManagerCallStopsOnIdle(t *testing.T) {
	srv := &Server{
		Name: "slow",
		Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) printf '{"text":"partial"}\n'; sleep 30 ;;
  esac
done`),
		Limits: sandbox.StreamLimits{Idle: 200 * time.Millisecond, Total: 30 * time.Second},
	}
	m := NewManager([]*Server{srv})
	out, err := m.Call(context.Background(), "mcp__slow__x", nil, "", nil)
	if err != nil {
		t.Fatalf("a stopped stream is not a failure: %v", err)
	}
	if !strings.Contains(out, "partial") || !strings.Contains(out, "going idle") {
		t.Errorf("result: %q", out)
	}
}

func TestManagerCallCancelledIsAnError(t *testing.T) {
	srv := &Server{Name: "slow", Bin: fakeServer(t, `while IFS= read -r line; do
  case "$line" in
    *tools/list*) printf '{"tools":[{"name":"x"}]}\n' ;;
    *tools/call*) sleep 30 ;;
  esac
done`)}
	m := NewManager([]*Server{srv})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := m.Call(ctx, "mcp__slow__x", nil, "", nil)
	if err == nil {
		t.Fatal("expected an error from a cancelled call")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("cancellation took %v", elapsed)
	}
}
