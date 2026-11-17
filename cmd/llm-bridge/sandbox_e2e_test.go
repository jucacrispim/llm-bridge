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

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"llm-bridge/sandbox"
)

// functionalToolSandbox returns the sandbox every tool runs under, built by the
// bridge's own configuration, and proves this machine can apply it.
//
// Needs the real caged binary, and a machine that can create a user namespace. A
// bridge inside another cage cannot, which is a property of the environment and
// not of the policy, so the test skips instead of failing.
func functionalToolSandbox(t *testing.T, cfg *sandboxConfig) *sandbox.Sandbox {
	t.Helper()
	bin := os.Getenv("CAGED_BIN")
	if bin == "" {
		found, err := exec.LookPath("caged")
		if err != nil {
			t.Skip("caged not found in $PATH and $CAGED_BIN unset")
		}
		bin = found
	}
	cfg.cagedBin = bin
	// validate runs the same namespace probe the bridge runs at startup, so a
	// skip here means the bridge would refuse to start too.
	if err := cfg.validate(); err != nil {
		t.Skipf("this machine cannot build the namespaces every tool needs: %v", err)
	}
	sb, err := cfg.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	return sb
}

// toolRun runs script with bash under the tool policy and fails on a non-zero
// exit: these probes are expected to work, so a failure is the result.
func toolRun(t *testing.T, sb *sandbox.Sandbox, script string) string {
	t.Helper()
	out, err := sb.Run(context.Background(), "", "bash", "-c", script)
	if err != nil {
		t.Fatalf("bash -c %q: %v (%s)", script, err, out)
	}
	return string(out)
}

// probeName returns a file name unlikely to collide with anything the host left
// in /tmp, so the "did it leak out" checks cannot pass on a stale file.
func probeName(t *testing.T) string {
	t.Helper()
	return "llm-bridge-e2e-" + strconv.Itoa(os.Getpid()) + "-" + filepath.Base(t.Name())
}

// TestFunctionalToolNamespaces is the check of what the private namespaces
// change for a tool: its own /tmp and /dev/shm, a network with only loopback,
// and nothing of it left on the host afterwards. Run it outside any cage.
func TestFunctionalToolNamespaces(t *testing.T) {
	sb := functionalToolSandbox(t, &sandboxConfig{})

	t.Run("private tmp", func(t *testing.T) {
		name := probeName(t) + ".tmp"
		hostPath := filepath.Join("/tmp", name)
		os.Remove(hostPath)
		defer os.Remove(hostPath)

		out := toolRun(t, sb, "echo hi > /tmp/"+name+" && cat /tmp/"+name)
		if strings.TrimSpace(out) != "hi" {
			t.Errorf("reading back the file inside the tool: got %q", out)
		}
		if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
			t.Errorf("%s is visible on the host (stat: %v): the tmpfs did not replace /tmp", hostPath, err)
		}
	})

	t.Run("private shm", func(t *testing.T) {
		if _, err := os.Stat("/dev/shm"); err != nil {
			t.Skipf("no /dev/shm on this host: %v", err)
		}
		name := probeName(t) + ".shm"
		hostPath := filepath.Join("/dev/shm", name)
		os.Remove(hostPath)
		defer os.Remove(hostPath)

		toolRun(t, sb, "echo hi > /dev/shm/"+name)
		if _, err := os.Stat(hostPath); !os.IsNotExist(err) {
			t.Errorf("%s is visible on the host (stat: %v): the shm segment leaked", hostPath, err)
		}
	})

	// dd reads /dev/zero, so the policy has to grant it: without the device a
	// tool cannot write a file of a fixed size.
	t.Run("dd from /dev/zero", func(t *testing.T) {
		name := probeName(t) + ".dd"
		out := toolRun(t, sb, "dd if=/dev/zero of=/tmp/"+name+" bs=4096 count=1 2>&1 && wc -c < /tmp/"+name)
		if strings.TrimSpace(out) == "" || !strings.HasSuffix(strings.TrimSpace(out), "4096") {
			t.Errorf("dd from /dev/zero: got %q", out)
		}
	})

	t.Run("temp env points at the private tmp", func(t *testing.T) {
		out := toolRun(t, sb, `echo "$TMPDIR|$GOTMPDIR|$TMP|$TEMP"`)
		for _, dir := range strings.Split(strings.TrimSpace(out), "|") {
			if dir != privateTmp {
				t.Errorf("temp directory %q, want %q", dir, privateTmp)
			}
		}
		// mktemp honours TMPDIR, so the directory it prints is the private
		// one: a tool can build a work directory and lose it on exit.
		out = toolRun(t, sb, "d=$(mktemp -d) && cd \"$d\" && touch x && pwd")
		if !strings.HasPrefix(strings.TrimSpace(out), privateTmp+"/") {
			t.Errorf("mktemp -d outside the private %s: %q", privateTmp, out)
		}
	})

	// A server and a client in the same call: the namespace is per call, so
	// this is the only shape in which they can talk.
	t.Run("bind and connect in one call", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not in $PATH")
		}
		script := `python3 -c '
import functools, http.server, threading, urllib.request
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory="/tmp")
srv = http.server.HTTPServer(("127.0.0.1", 8000), handler)
threading.Thread(target=srv.serve_forever, daemon=True).start()
print(urllib.request.urlopen("http://127.0.0.1:8000/").status)
srv.shutdown()
' 2>/dev/null`
		out := toolRun(t, sb, script)
		if strings.TrimSpace(out) != "200" {
			t.Errorf("expected 200 from the local server, got %q", out)
		}
	})

	t.Run("only loopback exists", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not in $PATH")
		}
		out := toolRun(t, sb, `python3 -c 'import socket; print(socket.if_nameindex())'`)
		if strings.TrimSpace(out) != "[(1, 'lo')]" {
			t.Errorf("interfaces inside the namespace: %q, want only loopback", out)
		}
	})

	// ENETUNREACH rather than EACCES: the namespace has no route out, and the
	// seccomp policy allows the socket call itself.
	t.Run("no route out", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not in $PATH")
		}
		script := `python3 -c '
import errno, socket
try:
    socket.create_connection(("1.1.1.1", 443), timeout=3)
    print("CONNECTED")
except OSError as err:
    print(errno.errorcode[err.errno])
'`
		out := toolRun(t, sb, script)
		if got := strings.TrimSpace(out); got != "ENETUNREACH" {
			t.Errorf("connecting to 1.1.1.1:443: %q, want ENETUNREACH", got)
		}
	})

	// The other half of the same configuration: with -sandbox-tmp the private
	// /tmp is a bind mount of a host directory, and the file survives.
	t.Run("tmp bind exchanges files with the host", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "shared")
		bound := functionalToolSandbox(t, &sandboxConfig{tmpDir: dir})
		name := probeName(t) + ".bind"
		toolRun(t, bound, "echo hi > /tmp/"+name)
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s from the host: %v", name, err)
		}
		if strings.TrimSpace(string(b)) != "hi" {
			t.Errorf("file content: %q", b)
		}
	})
}
