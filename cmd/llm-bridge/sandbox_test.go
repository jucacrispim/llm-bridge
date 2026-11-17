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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"llm-bridge/sandbox"
	"llm-bridge/tools"
)

// TestEnsureTempDirCreates checks the directory is created with 0700 and that a
// second call is a no-op (the bridge may run it again on every start).
func TestEnsureTempDirCreates(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "llm-bridge")
	c := &sandboxConfig{tmpDir: dir}

	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("ensureTempDir: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", dir)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("permissions: got %o want 0700", perm)
	}
	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("second ensureTempDir: %v", err)
	}
}

// TestEnsureTempDirRejectsFile checks a path already taken by a regular file is
// a startup error instead of being silently ignored.
func TestEnsureTempDirRejectsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{tmpDir: path}
	if err := c.ensureTempDir(); err == nil {
		t.Fatal("expected an error when the temp path is a file")
	}
}

// TestEnsureTempDirEmpty checks an empty -sandbox-tmp is a no-op: the private
// /tmp is a fresh tmpfs, so there is no bind source to create.
func TestEnsureTempDirEmpty(t *testing.T) {
	c := &sandboxConfig{}
	if err := c.ensureTempDir(); err != nil {
		t.Fatalf("ensureTempDir: %v", err)
	}
}

// TestEnsureBridgeDir checks ~/.llm-bridge is created at startup: caged exits
// 125 on a granted path it cannot open, and the directory is granted so the
// tools can write the context and the hooks.
func TestEnsureBridgeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := (&sandboxConfig{}).ensureBridgeDir(); err != nil {
		t.Fatalf("ensureBridgeDir: %v", err)
	}
	info, err := os.Stat(filepath.Join(home, ".llm-bridge"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("%s is not a directory", info.Name())
	}
}

// TestToolPathsGrantsPrivateVolumes checks /tmp and /dev/shm are granted
// read-write. With --private-tmp and --private-shm the rules are resolved after
// the mounts, so the grant names the private ones.
func TestToolPathsGrantsPrivateVolumes(t *testing.T) {
	ro, rw := (&sandboxConfig{}).toolPaths()
	for _, path := range []string{privateTmp, "/dev/shm"} {
		if !contains(rw, path) {
			t.Errorf("%q not granted read-write: %v", path, rw)
		}
		if contains(ro, path) {
			t.Errorf("%q granted read-only too: %v", path, ro)
		}
	}
}

// TestToolPathsGrantsBridgeDir checks ~/.llm-bridge moved from read-only to
// read-write: the tools write the context and the hook scripts there.
func TestToolPathsGrantsBridgeDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ro, rw := (&sandboxConfig{}).toolPaths()
	dir := filepath.Join(home, ".llm-bridge")
	if !contains(rw, dir) {
		t.Errorf("%q not granted read-write: %v", dir, rw)
	}
	if contains(ro, dir) {
		t.Errorf("%q still granted read-only: %v", dir, ro)
	}
}

// TestToolSandboxTempEnv checks the tools get the temporary-directory block on
// top of caged's base environment, pointing at the private /tmp: without it a
// program would fall back to a path the policy does not grant.
func TestToolSandboxTempEnv(t *testing.T) {
	c := &sandboxConfig{cagedBin: fakeCagedPath(t)}

	sb, err := c.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	for _, name := range tempEnvNames {
		want := name + "=" + privateTmp
		if !contains(sb.Policy.Env, want) {
			t.Errorf("missing %q in policy env: %v", want, sb.Policy.Env)
		}
		// It must reach caged as --env, not only in the Go struct.
		if !contains(sb.Policy.Args(), want) {
			t.Errorf("missing %q in caged args: %v", want, sb.Policy.Args())
		}
	}
	if !contains(sb.Policy.ReadWrite, privateTmp) {
		t.Errorf("private /tmp not granted to the tools: %v", sb.Policy.ReadWrite)
	}
}

// TestTempEnvPointsAtPrivateTmp checks every convention points at /tmp, the
// path the private mount names.
func TestTempEnvPointsAtPrivateTmp(t *testing.T) {
	env := tempEnv()
	if len(env) != len(tempEnvNames) {
		t.Fatalf("env = %v, want one entry per name", env)
	}
	for _, kv := range env {
		if !strings.HasSuffix(kv, "="+privateTmp) {
			t.Errorf("entry %q does not point at %s", kv, privateTmp)
		}
	}
}

// fakeCagedPath returns the path of a stub caged binary: enough for
// sandboxConfig.binary(), which only resolves the path.
func fakeCagedPath(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caged")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// contains reports whether list has exactly the entry s.
func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// TestToolSandboxPrivateNamespaces checks what every tool gets: a fresh tmpfs
// for /tmp and /dev/shm, and a loopback-only network. The socket families and
// any-port connect/bind come with it, because the namespace is what bounds
// where a connection can go.
func TestToolSandboxPrivateNamespaces(t *testing.T) {
	c := &sandboxConfig{cagedBin: fakeCagedPath(t)}
	sb, err := c.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	p := sb.Policy
	if !p.PrivateTmp || p.PrivateTmpPath != "" {
		t.Errorf("expected a fresh tmpfs for /tmp: %+v", p)
	}
	if !p.PrivateShm {
		t.Error("expected a private /dev/shm")
	}
	if !p.LoopbackOnly || !p.AllowNetworkSockets || !p.ConnectTCP || !p.BindTCP {
		t.Fatalf("loopback policy is incomplete: %+v", p)
	}
	if len(p.ConnectPorts) > 0 || len(p.BindPorts) > 0 {
		t.Errorf("port lists are meaningless with any-port on: %+v", p)
	}
	want := []string{
		"--private-tmp",
		"--private-shm",
		"--loopback-only",
		"--allow-network-sockets",
		"--allow-connect-tcp",
		"--allow-bind-tcp",
	}
	args := p.Args()
	for _, flag := range want {
		if !contains(args, flag) {
			t.Errorf("%s missing from caged args: %v", flag, args)
		}
	}
}

// TestToolSandboxTmpBind checks -sandbox-tmp turns the private /tmp into a bind
// mount of that directory, so the host can exchange files with the tools.
func TestToolSandboxTmpBind(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), tmpDir: dir}
	sb, err := c.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	if sb.Policy.PrivateTmpPath != dir {
		t.Fatalf("PrivateTmpPath = %q, want %q", sb.Policy.PrivateTmpPath, dir)
	}
	args := sb.Policy.Args()
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--private-tmp" && args[i+1] == dir {
			return
		}
	}
	t.Errorf("--private-tmp %s missing from caged args: %v", dir, args)
}

// TestCurlSandboxKeepsRealNetwork checks the curl tool gets neither the
// loopback namespace (a curl bounded by loopback fetches nothing) nor the
// private volumes. Its own minimal filesystem policy is the bound.
func TestCurlSandboxKeepsRealNetwork(t *testing.T) {
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), curl: true}
	sb, err := c.curlSandbox()
	if err != nil {
		t.Fatalf("curlSandbox: %v", err)
	}
	if sb.Policy.LoopbackOnly || sb.Policy.PrivateTmp || sb.Policy.PrivateShm {
		t.Fatalf("curl must keep real network access and the host's /tmp: %+v", sb.Policy)
	}
	for _, flag := range []string{"--loopback-only", "--private-tmp", "--private-shm"} {
		if contains(sb.Policy.Args(), flag) {
			t.Errorf("curl args carry %s: %v", flag, sb.Policy.Args())
		}
	}
}

// TestToolSandboxGrantsToolBin checks the llm-tools binary is inside the tool
// policy: the filesystem tools run as it, so without the grant every
// read/write/glob/search_replace would fail to exec.
func TestToolSandboxGrantsToolBin(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin", "llm-tools")
	// existing() filters missing paths, and the real binary always exists.
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), tmpDir: t.TempDir(), toolBin: bin}
	sb, err := c.toolSandbox()
	if err != nil {
		t.Fatalf("toolSandbox: %v", err)
	}
	want := filepath.Dir(bin)
	for _, p := range sb.Policy.ReadOnly {
		if p == want {
			return
		}
	}
	t.Errorf("the llm-tools directory %q is not granted (read-only: %v)", want, sb.Policy.ReadOnly)
}

// TestCurlSandboxMinimalFilesystem checks the curl sandbox is the inverse of the
// tools one: network on, but nothing of the user's to read. The shared policy
// (projects, home, KB, temp dir) must not leak into it — network plus a readable
// project is an exfiltration primitive.
func TestCurlSandboxMinimalFilesystem(t *testing.T) {
	tmp := t.TempDir()
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), tmpDir: tmp, curl: true}

	sb, err := c.curlSandbox()
	if err != nil {
		t.Fatalf("curlSandbox: %v", err)
	}
	if !sb.Policy.AllowNetworkSockets {
		t.Error("curl must have the socket families allowed")
	}
	if sb.Policy.KeepEnv {
		t.Error("curl must not inherit the bridge's environment (API keys)")
	}
	if got := sb.Policy.ConnectPorts; len(got) != 2 || got[0] != 443 || got[1] != 80 {
		t.Errorf("ConnectPorts = %v, want [443 80]", got)
	}
	if sb.Policy.ConnectTCP {
		t.Error("any-port must be opt-in")
	}
	if got := sb.Policy.ReadWrite; len(got) != 1 || got[0] != "/dev/null" {
		t.Errorf("ReadWrite = %v, want [/dev/null] only", got)
	}
	if contains(sb.Policy.ReadWrite, tmp) {
		t.Errorf("curl must not get the temp dir: %v", sb.Policy.ReadWrite)
	}
	home, _ := os.UserHomeDir()
	for _, p := range sb.Policy.ReadOnly {
		if home != "" && (p == home || strings.HasPrefix(p, home+"/")) {
			t.Errorf("curl must not read the home directory: %q", p)
		}
	}
	for _, want := range []string{"/usr", "/etc"} {
		if !contains(sb.Policy.ReadOnly, want) {
			t.Errorf("curl needs %q to run (binary, CAs, resolv.conf): %v", want, sb.Policy.ReadOnly)
		}
	}
}

// TestCurlSandboxAnyPort checks -curl-any-port swaps the port rule for
// --allow-connect-tcp (the two are mutually exclusive in caged).
func TestCurlSandboxAnyPort(t *testing.T) {
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), curl: true, curlAnyPort: true}
	sb, err := c.curlSandbox()
	if err != nil {
		t.Fatalf("curlSandbox: %v", err)
	}
	if !sb.Policy.ConnectTCP {
		t.Error("ConnectTCP should be on with -curl-any-port")
	}
	if len(sb.Policy.ConnectPorts) > 0 {
		t.Errorf("ConnectPorts should be empty with -curl-any-port: %v", sb.Policy.ConnectPorts)
	}
}

// TestCurlSandboxExplicitPorts checks -curl-port replaces the defaults (and
// reaches caged as --allow-connect-port).
func TestCurlSandboxExplicitPorts(t *testing.T) {
	c := &sandboxConfig{cagedBin: fakeCagedPath(t), curl: true, curlPorts: []int{8443}}
	sb, err := c.curlSandbox()
	if err != nil {
		t.Fatalf("curlSandbox: %v", err)
	}
	if strings.Join(ints(sb.Policy.ConnectPorts), ",") != "8443" {
		t.Fatalf("ConnectPorts = %v, want [8443]", sb.Policy.ConnectPorts)
	}
	if !contains(sb.Policy.Args(), "--allow-connect-port") {
		t.Errorf("port rule missing from caged args: %v", sb.Policy.Args())
	}
}

// TestCurlSandboxDisabled checks the two ways of having no curl tool at all:
// -curl=false and -disable-sandbox. Both must yield a nil sandbox, which the
// server turns into "do not offer the tool".
func TestCurlSandboxDisabled(t *testing.T) {
	for name, c := range map[string]*sandboxConfig{
		"curl flag off": {cagedBin: fakeCagedPath(t), curl: false},
		"sandbox off":   {disabled: true, curl: true},
	} {
		sb, err := c.curlSandbox()
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if sb != nil {
			t.Errorf("%s: expected a nil sandbox, got %+v", name, sb)
		}
	}
}

// fakeCagedWithReportFD writes a stub caged whose --help advertises
// --report-fd, which is what validate() checks at startup.
func fakeCagedWithReportFD(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caged")
	script := "#!/bin/sh\necho 'usage: caged [--ro DIR] [--report-fd N] -- CMD'\nexit 0\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeToolsBin writes a stub llm-tools that answers the handshake, which is
// what validate() runs at startup (tools.CheckToolbox).
func fakeToolsBin(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "llm-tools")
	script := "#!/bin/sh\ncat > /dev/null\nprintf '{\"ok\":true,\"result\":\"llm-tools/" +
		tools.ToolboxVersion + "\"}\\n'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestValidateRequiresToolbox pins the startup contract: a missing helper is an
// error like a missing caged, never a silent fallback to running the filesystem
// tools inside the bridge.
func TestValidateRequiresToolbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &sandboxConfig{
		cagedBin: fakeCagedWithReportFD(t),
		toolsBin: filepath.Join(t.TempDir(), "missing", "llm-tools"),
		tmpDir:   t.TempDir(),
	}
	if err := c.validate(); err == nil {
		t.Fatal("a missing llm-tools must fail the startup")
	}
}

// TestValidateRejectsForeignToolbox: a binary that does not answer the
// handshake is not llm-tools, whatever its name.
func TestValidateRejectsForeignToolbox(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "llm-tools")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho 'not me'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := &sandboxConfig{cagedBin: fakeCagedWithReportFD(t), toolsBin: path, tmpDir: t.TempDir()}
	if err := c.validate(); err == nil {
		t.Fatal("a foreign binary must fail the startup")
	}
}

// TestValidateLeavesToolboxUnsetWhenDisabled: with the sandbox off there is no
// policy to run the helper under, so the filesystem tools stay in-process.
func TestValidateLeavesToolboxUnsetWhenDisabled(t *testing.T) {
	c := &sandboxConfig{disabled: true, toolsBin: "/nonexistent/llm-tools"}
	if err := c.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if c.toolBin != "" {
		t.Errorf("toolBin = %q, want empty with the sandbox off", c.toolBin)
	}
}

// TestValidateAcceptsCurlConfig covers the happy path of validate: caged with
// --report-fd, curl ports in range.
func TestValidateAcceptsCurlConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &sandboxConfig{cagedBin: fakeCagedWithReportFD(t), toolsBin: fakeToolsBin(t), curlPorts: []int{443, 8443}}
	if err := c.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestValidateRejectsBadCurlPort covers the range check of -curl-port.
func TestValidateRejectsBadCurlPort(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, port := range []int{0, -1, 70000} {
		c := &sandboxConfig{cagedBin: fakeCagedWithReportFD(t), toolsBin: fakeToolsBin(t), curlPorts: []int{port}}
		if err := c.validate(); err == nil {
			t.Errorf("port %d: expected an error", port)
		}
	}
}

// TestRequireReportFDRejectsOldCaged covers the old-caged guard: without
// --report-fd a failed sandbox cannot be told apart from a command that exited
// with the same status, so it is a startup error.
func TestRequireReportFDRejectsOldCaged(t *testing.T) {
	err := requireReportFD(fakeCagedPath(t))
	if err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("err = %v, want 'too old'", err)
	}
	if err := requireReportFD(fakeCagedWithReportFD(t)); err != nil {
		t.Fatalf("new caged rejected: %v", err)
	}
}

// fakeCagedFailing writes a stub caged that answers --help (so the startup
// checks get past requireReportFD) and fails every real invocation the way the
// real one does: a message on the report descriptor and exit 125 for a setup
// that did not apply, or a message on stderr and exit 2 for an option it does
// not know (an old caged).
func fakeCagedFailing(t *testing.T, code int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "caged")
	script := "#!/bin/sh\n" +
		"case \"$1\" in --help) echo 'usage: caged [--ro DIR] [--report-fd N] -- CMD'; exit 0;; esac\n"
	if code == 125 {
		script += "echo 'caged: error setting up the mount namespace' >&3\nexit 125\n"
	} else {
		script += "echo \"caged: unrecognized option '--private-tmp'\" >&2\nexit 2\n"
	}
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestValidateProbesNamespaces covers the happy path: validate runs the
// throwaway caged probe with the namespaces every tool gets.
func TestValidateProbesNamespaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	c := &sandboxConfig{cagedBin: fakeCagedWithReportFD(t), toolsBin: fakeToolsBin(t)}
	if err := c.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

// TestValidateRejectsBrokenNamespaces pins the probe's reason to exist: a caged
// that cannot build the namespaces (AppArmor, unprivileged_userns_clone=0, the
// bridge itself inside a cage) or that does not know a flag must stop the
// bridge at startup, not turn every tool call into a *SetupError.
func TestValidateRejectsBrokenNamespaces(t *testing.T) {
	for name, code := range map[string]int{
		"setup failed (exit 125)":   sandbox.ExitSetupFailed,
		"old caged (usage, exit 2)": sandbox.ExitUsage,
	} {
		t.Setenv("HOME", t.TempDir())
		c := &sandboxConfig{cagedBin: fakeCagedFailing(t, code), toolsBin: fakeToolsBin(t)}
		err := c.validate()
		if err == nil {
			t.Errorf("%s: expected an error", name)
			continue
		}
		if !strings.Contains(err.Error(), "private namespaces") {
			t.Errorf("%s: error should say what failed: %v", name, err)
		}
	}
}

// ints converts a []int to []string, for compact comparisons in tests.
func ints(v []int) []string {
	out := make([]string, 0, len(v))
	for _, n := range v {
		out = append(out, strconv.Itoa(n))
	}
	return out
}
