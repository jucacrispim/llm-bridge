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

// Sandboxing is ON by default and gives every process the bridge starts exactly
// one policy: the bridge itself runs unconfined, and each tool it spawns is a
// caged child with the policy built for it.
//
// It used to be two layers — the bridge re-executed itself under a loose
// "ceiling" and the tools ran under a stricter one, Landlock intersecting the
// two. That is gone, for one reason: a process that is already confined cannot
// give its child a namespace. caged's own namespace support needs
// /proc/self/uid_map written during setup; from inside a ceiling that file
// cannot be granted (Landlock rules are per inode, and the grant would resolve
// to the ceiling's own uid_map) and must not be granted either (/proc/<pid>/
// environ of the bridge carries the API keys). One policy per process is what
// makes namespaces possible, and the price is that the bridge is no longer
// bounded by the kernel itself — which is why no tool that takes a path from
// the model may run inside it (see llm-tools).
//
// The filesystem policy is shared in spirit (a tool works on the projects, so
// it needs what the bridge needs), while the environment (API keys), the IPC
// and the network are what the tools are denied.
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
// -disable-sandbox turns it off. A missing caged binary is a startup error
// rather than a silent fallback to unsandboxed execution.
//
// These defaults are a starting point; -sandbox-ro/-sandbox-rw extend them.

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"llm-bridge/sandbox"
	"llm-bridge/tools"
)

// toolboxHandshakeTimeout bounds the startup check of the llm-tools binary: it
// answers one request and exits, so anything slower than this is a broken
// binary, not a slow one.
const toolboxHandshakeTimeout = 5 * time.Second

// defaultTempDir is the temporary directory the tools use. /tmp itself is NOT
// granted (any process could read another one's scratch files); a dedicated
// directory gives programs that need a writable temp dir — go build/test,
// mkstemp, gcc, git — a place that the policy actually allows.
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
	// on by default: every tool the bridge spawns runs under caged.
	disabled bool
	// cagedBin is the path of the caged binary (empty: $CAGED_BIN, then
	// $PATH).
	cagedBin string
	// ro/rw extend the read-only/read-write lists of the tool policies.
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
	// toolsBin is the llm-tools binary (empty: next to the bridge, then
	// $PATH). It runs the filesystem tools out of process.
	toolsBin string
	// toolBin is toolsBin as resolved by validate(): absolute, verified and
	// handshake-checked. Empty when the sandbox is off.
	toolBin string
	// tmpDir is the temporary directory granted read-write to the tools and
	// exported as TMPDIR/GOTMPDIR/TMP/TEMP (default defaultTempDir).
	tmpDir string
	// loopback gives the tools their own network namespace with only loopback
	// up: they can talk to a server they start themselves, and to nothing
	// else (-sandbox-loopback-only).
	loopback bool
}

// binary resolves the caged binary: the -caged-bin flag, then $CAGED_BIN, then
// $PATH. It returns an absolute path, since a policy has to grant access to the
// binary's directory.
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

// toolsBinary resolves the llm-tools binary: the -tools-bin flag, then the
// directory of the bridge itself, then $PATH.
//
// Next to the bridge comes before $PATH on purpose: the helper answers a
// protocol the bridge defines, so the one that ships with this bridge is the
// one to run. A stray llm-tools earlier in the PATH would be a different build
// (the handshake below catches it, but picking the right file is better than
// failing on it).
func (c *sandboxConfig) toolsBinary() (string, error) {
	if c.toolsBin != "" {
		return filepath.Abs(c.toolsBin)
	}
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve own executable: %w", err)
	}
	cand := filepath.Join(filepath.Dir(self), "llm-tools")
	if _, statErr := os.Stat(cand); statErr == nil {
		return cand, nil
	}
	found, err := exec.LookPath("llm-tools")
	if err != nil {
		return "", fmt.Errorf("llm-tools not found next to %s nor in $PATH (use -tools-bin)", self)
	}
	return found, nil
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

// toolPaths returns the read-only and read-write paths of the shell/grep/hooks
// policy: the system trees, the knowledge-base locations (assets read-only,
// per-project data read-write) and whatever -sandbox-ro/-sandbox-rw added.
//
// No project directory is granted by default: the project changes at runtime
// (set_cwd), so whoever starts the bridge passes the projects root with
// -sandbox-rw.
func (c *sandboxConfig) toolPaths() (ro, rw []string) {
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
	// The temp dir is writable by the tools: they run go build/test, mkstemp
	// and friends. It is created at startup (see ensureTempDir), so it exists
	// by the time caged opens it.
	if c.tmpDir != "" {
		rw = append(rw, c.tmpDir)
	}
	rw = append(rw, c.rw...)
	return ro, rw
}

// ensureTempDir creates the temporary directory at startup: caged opens every
// granted path with O_PATH when it builds a policy, so a missing one makes it
// fail with exit 125 (and the first shell/grep call would fail as a setup
// error).
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
// convention at dir. The tools get caged's base env, with no TMPDIR at all, so
// without this a tool would fall back to /tmp and be denied.
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

// toolSandbox builds the sandbox for the external processes the bridge spawns
// (shell, grep, hooks). It returns nil when the sandbox is off, which means
// "run the tools as before".
//
// A tool gets a filesystem policy that lets it work on the projects and on the
// knowledge base, and it is denied the environment the bridge holds (no
// KeepEnv: caged's small base set only, so the API keys never reach it), the
// IPC (unix sockets and SysV IPC are denied by default) and the network
// (seccomp denies the socket families by default; -sandbox-loopback-only
// releases them inside a namespace whose only interface is loopback, and real
// outbound access belongs to the curl tool alone, see curlSandbox).
//
// The one thing added to the environment is the temporary-directory block:
// caged's base set has no TMPDIR, so without it programs that need a scratch
// dir (go build/test, mkstemp, gcc, git) would fall back to /tmp, which the
// policy denies.
//
// -sandbox-loopback-only is the single exception to "the tools have no
// network", and it is a narrow one: the socket families are released (they have
// to be, or seccomp denies socket(AF_INET) before anything else) together with
// any-port connect and bind, but the process is in a network namespace that
// contains only loopback. The bound is topological, not a filter: an address
// outside 127.0.0.0/8 does not exist in there. That is the only way to say
// "localhost only" — Landlock has no address field and seccomp cannot read the
// sockaddr — and it is what a tool that starts a test server and talks to it in
// the same command needs. It does not reach the curl sandbox: a loopback-only
// curl could fetch nothing.
func (c *sandboxConfig) toolSandbox() (*sandbox.Sandbox, error) {
	if c.disabled {
		return nil, nil
	}
	caged, err := c.binary()
	if err != nil {
		return nil, err
	}
	ro, rw := c.toolPaths()
	// The llm-tools binary has to be readable and executable inside this
	// policy: the filesystem tools run as it, so without the grant every
	// read/write/glob/search_replace would fail to exec.
	if c.toolBin != "" {
		ro = append(ro, filepath.Dir(c.toolBin))
	}
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
	if c.loopback {
		// The namespace is the bound: releasing the families and the ports is
		// safe precisely because there is nowhere to go. Each tool run gets a
		// fresh namespace, so a server started by one call is not reachable
		// from the next.
		p.LoopbackOnly = true
		p.AllowNetworkSockets = true
		p.ConnectTCP = true
		p.BindTCP = true
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

// loopbackProbeTimeout bounds the startup probe of -sandbox-loopback-only.
// caged's namespace setup is a handful of syscalls; anything slower than this
// means it is not going to work at all.
const loopbackProbeTimeout = 10 * time.Second

// probeLoopback runs one throwaway command under --loopback-only, so the
// namespace is built (and the command run) before the first tool call.
//
// The flag cannot be checked the way --report-fd is: a --help that lists
// --loopback-only says nothing about whether this process can actually create a
// namespace. unshare(CLONE_NEWUSER) fails with EPERM or EINVAL for reasons that
// are all environmental (unprivileged_userns_clone=0, an AppArmor profile such
// as the one Ubuntu 23.10+ ships, a bridge that is itself running inside another
// cage), and the setup also needs to write /proc/self/uid_map. A bridge that
// accepted the flag and then failed in every tool call would turn a startup
// problem into a hundred *SetupErrors; this way it stops at startup, like a
// missing binary.
//
// The exit status catches an old caged (unknown option: usage error, exit 2) and
// the report descriptor catches a setup that did not apply (exit 125); both come
// back as an error from Run.
func probeLoopback(bin string) error {
	sb, err := sandbox.New(bin, sandbox.Policy{
		ReadOnly: existing(systemReadOnly),
		// LoopbackOnly alone: the probe is about the namespace, and granting
		// nothing it does not need keeps it from failing for another reason.
		LoopbackOnly: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), loopbackProbeTimeout)
	defer cancel()
	if _, err := sb.Run(ctx, "", "/bin/true"); err != nil {
		return fmt.Errorf("-sandbox-loopback-only: caged %s could not build a private network namespace (it needs unshare(CLONE_NEWUSER), a writable /proc/self/uid_map and unshare(CLONE_NEWNET); a bridge that is itself running inside a cage cannot): %w", bin, err)
	}
	return nil
}

// validate resolves the caged binary early, so a missing or misspelled path
// does not surface only when the first tool runs. With -disable-sandbox
// nothing is resolved and the binary is not required.
func (c *sandboxConfig) validate() error {
	if c.disabled {
		// No sandbox means no policy to run the helper under, so the
		// filesystem tools stay in-process: a process boundary without a
		// policy buys nothing.
		c.toolBin = ""
		return nil
	}
	bin, err := c.binary()
	if err != nil {
		return err
	}
	if err := requireReportFD(bin); err != nil {
		return err
	}
	// Checked by running it, not by reading --help: see probeLoopback.
	if c.loopback {
		if err := probeLoopback(bin); err != nil {
			return err
		}
	}
	// Resolved and checked here, at startup, for the same reason as the caged
	// binary: a missing or mismatched helper must stop the bridge at startup,
	// never degrade a tool call later.
	toolBin, err := c.toolsBinary()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), toolboxHandshakeTimeout)
	defer cancel()
	if err := tools.CheckToolbox(ctx, toolBin); err != nil {
		return err
	}
	c.toolBin = toolBin
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
// fill. Logging is not wired yet at this point, so errors go to stderr.
func registerSandboxFlags() *sandboxConfig {
	c := &sandboxConfig{}
	flag.BoolVar(&c.disabled, "disable-sandbox", false,
		"turn the process sandbox off; by default the bridge runs the shell/grep tools and hooks under caged (no network, no IPC, no inherited environment)")
	flag.StringVar(&c.cagedBin, "caged-bin", "",
		"path of the caged binary (default: $CAGED_BIN, then $PATH)")
	flag.StringVar(&c.toolsBin, "tools-bin", "",
		"path of the llm-tools binary, which runs the filesystem tools (read/write/glob/search_replace) under the tool sandbox (default: next to this binary, then $PATH)")
	flag.StringVar(&c.tmpDir, "sandbox-tmp", defaultTempDir,
		"temporary directory granted read-write to the bridge and the tools, exported as TMPDIR/GOTMPDIR/TMP/TEMP (created if missing; /tmp itself is not granted)")
	flag.BoolVar(&c.loopback, "sandbox-loopback-only", false,
		"give the tools their own network namespace with only loopback up: a tool can reach a server it starts itself (test servers, local ports) and nothing outside, so DNS does not resolve and no data leaves; the curl tool is unaffected")
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
