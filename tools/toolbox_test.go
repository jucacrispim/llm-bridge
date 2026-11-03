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

// The llm-tools protocol, both ends: what the helper answers (Serve) and what
// the bridge does with the answer (Execute through a sandbox, CheckToolbox).
//
// The helper is faked by a shell script, the same way sandbox_test.go fakes
// caged: the point here is the protocol and the routing, not the tool bodies,
// which the rest of the package tests in-process.

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"llm-bridge/sandbox"
)

// fakeToolbox writes an executable that answers one request with body.
func fakeToolbox(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm-tools")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIsFileTool(t *testing.T) {
	for _, name := range []string{"read", "write", "glob", "search_replace"} {
		if !IsFileTool(name) {
			t.Errorf("%s must be served by llm-tools", name)
		}
	}
	// shell/grep/curl exec external programs: they must run as the sandboxed
	// command itself, never as a child of a helper confined by the tool policy.
	for _, name := range []string{"shell", "grep", "curl", "knowledge", "nope"} {
		if IsFileTool(name) {
			t.Errorf("%s must not be served by llm-tools", name)
		}
	}
}

func TestServeHandshake(t *testing.T) {
	var out bytes.Buffer
	if err := Serve(strings.NewReader(`{"name":"__ping"}`), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	want := `{"ok":true,"result":"` + toolboxProduct + "/" + ToolboxVersion + `"}`
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("handshake: got %s, want %s", got, want)
	}
}

func TestServeFileTool(t *testing.T) {
	dir := t.TempDir()
	writeExecFile(t, filepath.Join(dir, "f.txt"), "hello\n")

	// The helper resolves relative paths against its working directory, which
	// is the bridge's cwd for the call; use an absolute one here.
	req := `{"name":"read","args":{"path":` + strconv.Quote(filepath.Join(dir, "f.txt")) + `}}`
	var out bytes.Buffer
	if err := Serve(strings.NewReader(req), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp toolboxResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	if !resp.OK || resp.Result != "hello\n" {
		t.Errorf("got ok=%v result=%q error=%q", resp.OK, resp.Result, resp.Error)
	}
}

func TestServeRejectsNonFileTool(t *testing.T) {
	var out bytes.Buffer
	if err := Serve(strings.NewReader(`{"name":"shell","args":{"command":"id"}}`), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp toolboxResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.Contains(resp.Error, "unsupported tool") {
		t.Errorf("shell must be refused, got ok=%v error=%q", resp.OK, resp.Error)
	}
}

func TestServeToolErrorIsReported(t *testing.T) {
	req := `{"name":"read","args":{"path":"/nonexistent/definitely/missing"}}`
	var out bytes.Buffer
	if err := Serve(strings.NewReader(req), &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	var resp toolboxResponse
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error == "" {
		t.Errorf("a missing file must come back as an error, got ok=%v", resp.OK)
	}
}

// One response per request, in order, until the input ends.
func TestServeAnswersEveryRequest(t *testing.T) {
	in := strings.NewReader("{\"name\":\"__ping\"}\n{\"name\":\"glob\",\"args\":{\"pattern\":\"/nonexistent/*\"}}\n")
	var out bytes.Buffer
	if err := Serve(in, &out); err != nil {
		t.Fatalf("serve: %v", err)
	}
	if n := len(strings.Split(strings.TrimSpace(out.String()), "\n")); n != 2 {
		t.Errorf("got %d responses, want 2:\n%s", n, out.String())
	}
}

func TestServeStopsOnMalformedRequest(t *testing.T) {
	var out bytes.Buffer
	err := Serve(strings.NewReader("{not json}\n"), &out)
	if err == nil {
		t.Fatal("a malformed request must be reported, not skipped")
	}
	if out.Len() != 0 {
		t.Errorf("nothing must be answered for a malformed request, got %q", out.String())
	}
}

func TestToolboxRequestLine(t *testing.T) {
	// A malformed payload becomes an empty object: the tool then reports its
	// own missing field, which is more useful than a JSON parse error.
	line, err := toolboxRequestLine("read", "not json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"args":{}`) {
		t.Errorf("malformed args must degrade to {}, got %s", line)
	}

	line, err = toolboxRequestLine("read", `{"path":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(line), `"path":"x"`) {
		t.Errorf("args must travel untouched, got %s", line)
	}
	if !strings.HasSuffix(string(line), "\n") {
		t.Errorf("a request must be one line, got %q", line)
	}
}

func TestDecodeToolboxResponse(t *testing.T) {
	if got, err := decodeToolboxResponse([]byte(`{"ok":true,"result":"x"}` + "\n")); err != nil || got != "x" {
		t.Errorf("ok response: got %q, %v", got, err)
	}
	if _, err := decodeToolboxResponse([]byte(`{"ok":false,"error":"boom"}`)); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error response: got %v", err)
	}
	if _, err := decodeToolboxResponse([]byte(`{"ok":false}`)); err == nil {
		t.Error("an empty error must not become a result")
	}
	// Garbage is a failure of the helper, never a tool result: silently
	// returning it as output would hide a broken sandbox.
	if _, err := decodeToolboxResponse([]byte("not json")); err == nil {
		t.Error("garbage output must be an error")
	}
	if _, err := decodeToolboxResponse(nil); err == nil {
		t.Error("empty output must be an error")
	}
}

// With a helper configured the filesystem tools run through it, under the tool
// policy: the request reaches the helper and its answer is the result.
func TestExecuteFileToolsOutOfProcess(t *testing.T) {
	dir := t.TempDir()
	requestPath := filepath.Join(dir, "request.json")
	toolBin := fakeToolbox(t, `cat > `+requestPath+`
printf '{"ok":true,"result":"from-helper"}\n'`)

	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithToolbox(dir, sb, nil, nil, toolBin)

	// None of these may be opened in-process: the helper answers everything.
	for _, call := range []struct{ name, args string }{
		{"read", `{"path":"whatever"}`},
		{"write", `{"path":"whatever","content":"x"}`},
		{"glob", `{"pattern":"*"}`},
		{"search_replace", `{"path":"x","search":"a","replace":"b"}`},
	} {
		out, err := e.Execute(context.Background(), call.name, call.args)
		if err != nil {
			t.Fatalf("%s: %v", call.name, err)
		}
		if out != "from-helper" {
			t.Errorf("%s: got %q, want the helper's answer", call.name, out)
		}
		if b, err := os.ReadFile(requestPath); err != nil || !strings.Contains(string(b), `"name":"`+call.name+`"`) {
			t.Errorf("%s: helper did not receive the call (request: %q, err %v)", call.name, b, err)
		}
	}
}

// A setup failure is not a tool result: it must come back as a *SetupError so
// the caller shows an error instead of a bogus result to the model.
func TestExecuteFileToolsOutOfProcessSetupError(t *testing.T) {
	toolBin := fakeToolbox(t, `printf '{"ok":true,"result":"x"}'`)
	// caged writes to the report descriptor (fd 3) and dies: the helper never
	// ran, so nothing it would have said could be trusted.
	sb, err := sandbox.New(fakeCaged(t, `printf 'no landlock' >&3
exit 125`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithToolbox(t.TempDir(), sb, nil, nil, toolBin)

	out, err := e.Execute(context.Background(), "read", `{"path":"x"}`)
	if err == nil {
		t.Fatalf("a broken sandbox must not produce a result, got %q", out)
	}
	if !isSetupError(err) {
		t.Errorf("want *SetupError, got %T: %v", err, err)
	}
}

// A helper that dies leaves nothing trustworthy: the error must mention the
// helper, and the result must stay empty.
func TestExecuteFileToolsHelperFailure(t *testing.T) {
	toolBin := fakeToolbox(t, `echo "helper exploded" >&2
exit 1`)
	sb, err := sandbox.New(fakeCaged(t, `exec "$@"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithToolbox(t.TempDir(), sb, nil, nil, toolBin)

	out, err := e.Execute(context.Background(), "read", `{"path":"x"}`)
	if err == nil {
		t.Fatalf("a dead helper must not produce a result, got %q", out)
	}
	if !strings.Contains(err.Error(), "llm-tools") || !strings.Contains(err.Error(), "helper exploded") {
		t.Errorf("error must name the helper and keep its message, got %v", err)
	}
}

func TestCheckToolbox(t *testing.T) {
	good := fakeToolbox(t, `cat >/dev/null
printf '{"ok":true,"result":"`+toolboxProduct+`/1"}\n'`)
	if err := CheckToolbox(context.Background(), good); err != nil {
		t.Errorf("a matching helper must pass: %v", err)
	}

	stale := fakeToolbox(t, `cat >/dev/null
printf '{"ok":true,"result":"`+toolboxProduct+`/0"}\n'`)
	if err := CheckToolbox(context.Background(), stale); err == nil {
		t.Error("a helper speaking another protocol version must fail the startup")
	}

	foreign := fakeToolbox(t, `cat >/dev/null
echo "i am not llm-tools"`)
	if err := CheckToolbox(context.Background(), foreign); err == nil {
		t.Error("a binary that is not llm-tools must fail the startup")
	}

	if err := CheckToolbox(context.Background(), filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("a missing binary must fail the startup")
	}
}
