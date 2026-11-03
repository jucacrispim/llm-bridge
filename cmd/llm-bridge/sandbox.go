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

//go:build !test

package main

// notest

// Sandboxing is ON by default and lives in two layers, because Landlock
// rulesets stack by intersection (a child policy can only tighten the
// parent's):
//
//   - ceiling: the bridge re-executes itself under caged at startup.
//     Everything it does — reading the project, writing the KB, talking to the
//     LLM over HTTPS — must fit the ceiling.
//   - tools: every external process the bridge spawns (the shell/grep tools
//     and hooks) runs under the same filesystem policy as the bridge, minus the
//     log file, and with no environment, no IPC and no network. Landlock
//     intersects the two, so nothing here has to know about the ceiling.
//
// The split is deliberate: the filesystem policy is shared (a tool works on the
// projects, so it needs what the bridge needs), while the environment (API
// keys), the IPC and the network are what the tools are denied.
//
// The one exception is the curl tool: it is the ONLY tool with network access,
// and it pays for it with a much narrower filesystem policy. curl runs under its
// own caged policy that grants the system trees and nothing else (no projects,
// no home, no temp directory), because network and "can read the user's files"
// together are a data-exfiltration primitive: with no local file to read, a
// fetch has nothing to leak beyond the conversation the model already has. A
// port allowlist (and, optionally, a host allowlist enforced by the bridge)
// bounds where it may go.
//
// -disable-sandbox turns both layers off. A missing caged binary is a startup
// error rather than a silent fallback to unsandboxed execution.
//
// These defaults are a starting point; -sandbox-ro/-sandbox-rw extend both
// layers at once.

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"llm-bridge/sandbox"
)

// sandboxedEnv marks the re-executed process so a nested launch (e.g. the
// bridge started by the Emacs client through caged) does not stack another
// identical layer.
const sandboxedEnv = "LLM_BRIDGE_SANDBOXED"

// defaultTempDir is the temporary directory the bridge and its tools use. /tmp
// itself is NOT granted (any process could read another one's scratch files);
// a dedicated directory gives programs that need a writable temp dir — go
// build/test, mkstemp, gcc, git — a place that the policy actually allows.
const defaultTempDir = "/tmp/llm-bridge"

// tempEnvNames are the environment variables that point at the temporary
// directory. TMPDIR is the POSIX convention and what Go's os.TempDir() reads;
// GOTMPDIR is the go command's own work directory; TMP and TEMP are the
// portable fallbacks some programs consult after TMPDIR (Perl's File::Spec,
// for instance).
var tempEnvNames = []string{"TMPDIR", "GOTMPDIR", "TMP", "TEMP"}

// defaultCurlPorts are the destinations the curl tool may reach when no
// -curl-port is given: HTTPS and HTTP, the two schemes the tool accepts.
var defaultCurlPorts = []int{443, 80}

// systemReadOnly are the trees a program needs to run at all: the binaries,
// their shared libraries and the system configuration (CAs for TLS,
// resolv.conf, nsswitch.conf, hosts).
var systemReadOnly = []string{"/usr", "/lib", "/lib64", "/bin", "/etc"}

// sandboxConfig holds the sandbox command-line options.
type sandboxConfig struct {
	// disabled turns the whole sandbox off (-disable-sandbox). The sandbox is
	// on by default: both layers (ceiling and tools) are always applied
	// together.
	disabled bool
	// cagedBin is the path of the caged binary (empty: $CAGED_BIN, then
	// $PATH).
	cagedBin string
	// ro/rw extend the read-only/read-write lists shared by both layers.
	ro []string
	rw []string
	// curl exposes the curl tool, the only tool with network access.
	curl bool
	// curlPorts are the TCP ports the curl tool may connect to (default
	// defaultCurlPorts; ignored with curlAnyPort).
	curlPorts []int
	// curlAnyPort lets the curl tool connect to any TCP port.
	curlAnyPort bool
	// curlHosts restricts the curl tool to these hosts (empty means any).
	curlHosts []string
	// legacyNetwork/legacyPorts are the removed -sandbox-network and
	// -sandbox-connect-port flags: still parsed (so an old configuration gets
	// a clear message instead of "flag provided but not defined") and rejected
	// by validate. Giving the tools network access as a whole is gone: only
	// the curl tool has it now.
	legacyNetwork bool
	legacyPorts   []int
	// logFile is the bridge log file, granted read+write to the ceiling as a
	// single file (never its directory).
	logFile string
	// tmpDir is the temporary directory granted read+write to both layers and
	// exported as TMPDIR/GOTMPDIR/TMP/TEMP (default defaultTempDir).
	tmpDir string
}

// binary resolves the caged binary: the -caged-bin flag, then $CAGED_BIN, then
// $PATH. It returns an absolute path, since the ceiling has to grant access to
// the binary's directory.
func (c *sandboxConfig) binary() (string, error) {
	path := c.cagedBin
	if path == "" {
		path = os.Getenv("CAGED_BIN")
	}
	if path == "" {
		found, err := exec.LookPath("caged")
		if err != nil {
			return "", fmt.Errorf("caged not found (use -caged-bin or $CAGED_BIN): %w", err)
		}
		path = found
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	return filepath.Abs(path)
}

// existing filters out paths that do not exist: caged opens every granted path
// before restricting itself, so a missing one makes it fail with exit 125.
func existing(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// sharedPaths returns the read-only and read-write paths used by BOTH layers:
// the system trees, the knowledge-base locations (assets read-only, per-project
// data read-write) and whatever -sandbox-ro/-sandbox-rw added.
//
// No project directory is granted: the ceiling is frozen at startup while the
// project changes at runtime (set_cwd), so whoever starts the bridge passes the
// projects root with -sandbox-rw. The tools inherit it from this same list,
// since they work on exactly the directories the bridge works on.
func (c *sandboxConfig) sharedPaths() (ro, rw []string) {
	home, _ := os.UserHomeDir()
	ro = append([]string{}, systemReadOnly...)
	// /dev/urandom is read-only on purpose: reading a character device needs
	// only read access, and the tools spawn programs that read randomness
	// (git writes objects through mkstemp, TLS handshakes). /dev/null, below,
	// is the one that has to be writable: `cmd 2>/dev/null` opens it for
	// writing, and read access alone would fail the open.
	ro = append(ro, "/dev/urandom")
	if home != "" {
		// KB assets (ONNX model, tokenizer, libonnxruntime).
		ro = append(ro, filepath.Join(home, ".cache", "llm-bridge"))
		// Global context (~/.llm-bridge/*.md) and global hooks
		// (~/.llm-bridge/hooks/*.sh). The bridge reads the context at startup
		// and the tools exec the hook scripts, which is exactly what read
		// access grants (read the file, execute it); without this both would
		// silently disappear once the sandbox is on.
		ro = append(ro, filepath.Join(home, ".llm-bridge"))
	}
	ro = append(ro, c.ro...)

	// /dev/null is writable on purpose: `cmd 2>/dev/null` opens it for writing
	// (read permission on it is not enough).
	rw = []string{"/dev/null"}
	if home != "" {
		// Per-project knowledge bases (data.json) and the bridge's own config.
		rw = append(rw, filepath.Join(home, ".local", "share", "llm-bridge"))
	}
	// The temp dir is writable by both layers: the tools run go build/test,
	// mkstemp and friends, and the bridge itself may use os.TempDir(). It is
	// created before the re-exec (see ensureTempDir), so it exists by the time
	// caged opens it.
	if c.tmpDir != "" {
		rw = append(rw, c.tmpDir)
	}
	rw = append(rw, c.rw...)
	return ro, rw
}

// ensureTempDir creates the temporary directory before the re-exec, for the
// same reason as ensureLogFile: caged opens every granted path with O_PATH when
// it builds the sandbox, so a missing one makes it fail with exit 125. It is
// also what makes the tools work when the bridge was already launched inside a
// sandbox (LLM_BRIDGE_SANDBOXED=1, no re-exec): without the directory the first
// shell/grep call would fail as a setup error.
//
// 0700 on purpose: it is this user's scratch space, and a world-writable
// directory in /tmp would let another user plant files (or symlinks) there.
// Failure is a startup error, never a silent fallback to /tmp, which is not
// granted at all.
func (c *sandboxConfig) ensureTempDir() error {
	if c.tmpDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.tmpDir, 0o700); err != nil {
		return fmt.Errorf("create temp directory %s: %w", c.tmpDir, err)
	}
	return nil
}

// tempEnv returns the environment entries that point every temporary-directory
// convention at dir. It is used for the re-executed bridge (whose env is
// inherited) and for the tools (which get caged's base env, with no TMPDIR at
// all, so without this a tool would fall back to /tmp and be denied).
func tempEnv(dir string) []string {
	if dir == "" {
		return nil
	}
	env := make([]string, 0, len(tempEnvNames))
	for _, name := range tempEnvNames {
		env = append(env, name+"="+dir)
	}
	return env
}

// isTempEnv reports whether kv sets one of the temporary-directory variables.
func isTempEnv(kv string) bool {
	for _, name := range tempEnvNames {
		if strings.HasPrefix(kv, name+"=") {
			return true
		}
	}
	return false
}

// ensureLogFile creates the log file (and its directory) before the re-exec:
// caged opens every granted path with O_PATH when it builds the sandbox, so a
// missing file makes it fail with exit 125. The descriptor itself does not
// survive the re-exec (caged closes everything above stderr); the file only has
// to exist so the sandboxed bridge can reopen it.
func ensureLogFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create log directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	return f.Close()
}

// ceilingPolicy builds the policy the whole bridge runs under: the shared paths
// plus the two binaries it must be able to execute and the log file, plus
// network access (the bridge must reach the LLM API).
func (c *sandboxConfig) ceilingPolicy(self, caged string) (sandbox.Policy, error) {
	ro, rw := c.sharedPaths()
	ro = append(ro, filepath.Dir(self), filepath.Dir(caged))

	// The log file is granted as a single file: opening an existing file for
	// append only needs write permission on the file, not on its directory,
	// and granting the directory (e.g. /tmp) would be far too broad. Only the
	// ceiling gets it — the tools have no business writing the bridge's log.
	if c.logFile != "" {
		if err := ensureLogFile(c.logFile); err != nil {
			return sandbox.Policy{}, err
		}
		rw = append(rw, c.logFile)
	}

	p := sandbox.Policy{
		ReadOnly:  existing(ro),
		ReadWrite: existing(rw),
		// The bridge needs the network: it talks to the LLM API over HTTPS.
		// The TCP port is not pinned because endpoints are configurable
		// (DEEPSEEK_URL/GOOGLE_URL, proxies, local servers).
		AllowNetworkSockets: true,
		ConnectTCP:          true,
		// The environment carries the API keys: keep it. The tools do not
		// inherit it (their policy has no KeepEnv).
		KeepEnv: true,
	}
	if err := p.Validate(); err != nil {
		return sandbox.Policy{}, err
	}
	return p, nil
}

// enterCeiling re-executes the bridge under caged (the outer layer). It must
// run before the log file is opened and before any goroutine starts: caged
// closes every inherited descriptor above stderr, and os.Executable() has to
// be resolved while /proc is still readable (the ceiling does not grant it).
func (c *sandboxConfig) enterCeiling() error {
	if c.disabled {
		return nil
	}
	if os.Getenv(sandboxedEnv) == "1" {
		// Already inside a sandbox (e.g. launched by the client through
		// caged): stacking another layer would only duplicate it.
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}
	caged, err := c.binary()
	if err != nil {
		return err
	}
	policy, err := c.ceilingPolicy(self, caged)
	if err != nil {
		return err
	}
	argv := append(policy.Args(), "--", self)
	argv = append(argv, os.Args[1:]...)
	// syscall.Exec replaces this process: same pid, same stdio pipes, so the
	// client's JSON-lines protocol is unaffected.
	return syscall.Exec(caged, append([]string{caged}, argv...), reexecEnv(os.Environ(), c.tmpDir))
}

// reexecEnv returns the environment for the re-executed bridge: this process's
// own environment plus the guard, so nothing the parent exported is lost.
//
// The temporary-directory variables are the exception: they are set from tmpDir
// (replacing whatever the parent exported), because they have to point at the
// one temp directory the ceiling grants — an inherited TMPDIR=/tmp would point
// at a path the sandbox denies.
//
// caged's own switches travel here too and are forwarded as the parent set
// them, never decided here: when the launcher (e.g. the Emacs client) exports
// CAGED_DISABLE_WARNINGS, caged inherits it and stays quiet about the Landlock
// ABI features the kernel lacks — that warning goes to stderr and a client
// waiting for the `ready` event may take it for a launch failure.
func reexecEnv(environ []string, tmpDir string) []string {
	env := make([]string, 0, len(environ)+len(tempEnvNames)+1)
	for _, kv := range environ {
		if strings.HasPrefix(kv, sandboxedEnv+"=") {
			continue
		}
		if tmpDir != "" && isTempEnv(kv) {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, tempEnv(tmpDir)...)
	return append(env, sandboxedEnv+"=1")
}

// toolSandbox builds the sandbox for the external processes the bridge spawns
// (shell, grep, hooks). It returns nil when the sandbox is off, which means
// "run the tools as before".
//
// A tool gets the same filesystem policy as the bridge (so it can work on the
// projects and on the knowledge base) but not the log file, and it is denied
// what the bridge is not: the environment (no KeepEnv: caged's small base set
// only, so the API keys never reach it), the IPC (unix sockets and SysV IPC are
// denied by default) and the network (seccomp denies the socket families: no
// flag turns it on for these tools — network access belongs to the curl tool
// alone, see curlSandbox).
//
// The one thing added to the environment is the temporary-directory block:
// caged's base set has no TMPDIR, so without it programs that need a scratch
// dir (go build/test, mkstemp, gcc, git) would fall back to /tmp, which the
// policy denies.
func (c *sandboxConfig) toolSandbox() (*sandbox.Sandbox, error) {
	if c.disabled {
		return nil, nil
	}
	caged, err := c.binary()
	if err != nil {
		return nil, err
	}
	ro, rw := c.sharedPaths()
	p := sandbox.Policy{
		ReadOnly:  existing(ro),
		ReadWrite: existing(rw),
		// No KeepEnv on purpose: the tools run with caged's small base
		// environment, so the API keys do not leak into them even though the
		// bridge itself was started with --keep-env.
		// The temporary-directory block, on top of caged's base env: no
		// KeepEnv, so the API keys stay out, but tools still get a writable
		// scratch dir instead of falling back to a denied /tmp.
		Env: tempEnv(c.tmpDir),
	}
	return sandbox.New(caged, p)
}

// curlSandbox builds the sandbox for the curl tool: the only tool with network
// access, and this policy is the reason it can be the only one.
//
// curl gets the system trees and nothing else. No project, no home directory, no
// knowledge base, no temp dir: it cannot read a single file of the user's, so a
// fetch has nothing local to leak (no "at file", no file://, no cookie file, no
// config file). That is the whole point: network plus "can read the projects"
// together is an exfiltration primitive — the answer can be sent inside a URL
// path, a header or a DNS query, and no flag allowlist changes that. With
// nothing to read, the only data left to leak is the conversation the model
// already has.
//
// Writing is granted for /dev/null only: os/exec opens it for the child's stdin
// when none is set, and inside Landlock that open fails without the grant.
//
// The destination is bounded twice: by port, in the kernel (Landlock
// NET_CONNECT_TCP via -curl-port; Landlock has no host filtering at all, and a
// UDP DNS query is not covered by the port rule) and, optionally, by host — the
// bridge compares url.Hostname() with -curl-allow-host before exec.
//
// nil means "the curl tool is off" and it is not offered to the model.
func (c *sandboxConfig) curlSandbox() (*sandbox.Sandbox, error) {
	if c.disabled || !c.curl {
		return nil, nil
	}
	caged, err := c.binary()
	if err != nil {
		return nil, err
	}
	ro := append([]string{}, systemReadOnly...)
	ro = append(ro, "/dev/urandom")
	p := sandbox.Policy{
		ReadOnly:  existing(ro),
		ReadWrite: existing([]string{"/dev/null"}),
		// The socket families have to be allowed for any TCP connection to
		// exist at all; the port rules (or -curl-any-port) then narrow where it
		// may go. No KeepEnv on purpose: caged's base environment only, so the
		// API keys and the bridge's proxy variables never reach a fetch.
		AllowNetworkSockets: true,
		ConnectTCP:          c.curlAnyPort,
	}
	if !c.curlAnyPort {
		p.ConnectPorts = append([]int{}, c.curlPortsOrDefault()...)
	}
	return sandbox.New(caged, p)
}

// curlPortsOrDefault returns the TCP ports the curl tool may connect to:
// -curl-port values, or the defaults (HTTPS and HTTP).
func (c *sandboxConfig) curlPortsOrDefault() []int {
	if len(c.curlPorts) == 0 {
		return defaultCurlPorts
	}
	return c.curlPorts
}

// requireReportFD checks that the caged binary understands --report-fd, the
// descriptor the bridge uses to tell "the sandbox did not apply" from "the
// command exited with that status".
//
// No exit status can make that distinction: caged replaces itself with the
// command, so both share the status space and a tool like `ls` (exit 2) or any
// program exiting 125 is indistinguishable from a broken sandbox. An old caged
// therefore cannot be worked around — it is a startup error, like a missing
// binary, never a silent downgrade to guessing.
func requireReportFD(bin string) error {
	out, err := exec.Command(bin, "--help").CombinedOutput()
	if err != nil {
		return fmt.Errorf("caged %s --help: %w", bin, err)
	}
	if !strings.Contains(string(out), "--report-fd") {
		return fmt.Errorf("caged %s is too old: it has no --report-fd, without which a failed sandbox cannot be told apart from a command that exited with the same status (rebuild caged)", bin)
	}
	return nil
}

// validate resolves the caged binary early, so a missing or misspelled path
// does not surface only when the first tool runs. With -disable-sandbox
// nothing is resolved and the binary is not required.
func (c *sandboxConfig) validate() error {
	if c.disabled {
		return nil
	}
	bin, err := c.binary()
	if err != nil {
		return err
	}
	if err := requireReportFD(bin); err != nil {
		return err
	}
	if c.legacyNetwork || len(c.legacyPorts) > 0 {
		return fmt.Errorf("-sandbox-network and -sandbox-connect-port were removed: only the curl tool has network access now (use -curl, -curl-port, -curl-any-port and -curl-allow-host)")
	}
	for _, port := range c.curlPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("-curl-port %d out of range 1..65535", port)
		}
	}
	if err := c.ensureTempDir(); err != nil {
		return err
	}
	return nil
}

// registerSandboxFlags registers the sandbox flags and returns the config they
// fill. Logging is not wired yet at this point (the re-exec happens before the
// log file is opened), so errors go to stderr.
func registerSandboxFlags() *sandboxConfig {
	c := &sandboxConfig{}
	flag.BoolVar(&c.disabled, "disable-sandbox", false,
		"turn the process sandbox off; by default the bridge re-executes itself under caged (ceiling) and runs the shell/grep tools and hooks under a stricter caged policy (no network)")
	flag.StringVar(&c.cagedBin, "caged-bin", "",
		"path of the caged binary (default: $CAGED_BIN, then $PATH)")
	flag.StringVar(&c.tmpDir, "sandbox-tmp", defaultTempDir,
		"temporary directory granted read-write to the bridge and the tools, exported as TMPDIR/GOTMPDIR/TMP/TEMP (created if missing; /tmp itself is not granted)")
	flag.Func("sandbox-ro", "extra read-only path for the sandbox (bridge and tools; repeatable)", func(v string) error {
		c.ro = append(c.ro, v)
		return nil
	})
	flag.Func("sandbox-rw", "extra read-write path for the sandbox (bridge and tools; repeatable; pass your projects root here)", func(v string) error {
		c.rw = append(c.rw, v)
		return nil
	})
	flag.BoolVar(&c.curl, "curl", true,
		"expose the curl tool, the only one with network access; it runs in its own sandbox with no project, home or temp paths (so a fetch cannot read local files) and every call is confirmed by the client")
	flag.Func("curl-port", "TCP port the curl tool may connect to (repeatable; default 443 and 80)", func(v string) error {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid port %q: %w", v, err)
		}
		c.curlPorts = append(c.curlPorts, port)
		return nil
	})
	flag.BoolVar(&c.curlAnyPort, "curl-any-port", false,
		"let the curl tool connect to any TCP port (default: only -curl-port, 443 and 80)")
	flag.Func("curl-allow-host", "restrict the curl tool to this host (repeatable; a leading dot matches any subdomain; default: any host)", func(v string) error {
		c.curlHosts = append(c.curlHosts, v)
		return nil
	})
	// Removed flags, still parsed so an old configuration fails with a
	// sentence instead of "flag provided but not defined".
	flag.BoolVar(&c.legacyNetwork, "sandbox-network", false,
		"removed: only the curl tool has network access now (use -curl/-curl-port)")
	flag.Func("sandbox-connect-port", "removed: use -curl-port", func(v string) error {
		port, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("invalid port %q: %w", v, err)
		}
		c.legacyPorts = append(c.legacyPorts, port)
		return nil
	})
	return c
}
