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

// Sandboxing is on by default: every process the bridge spawns gets exactly one
// policy. The bridge itself runs unconfined, so no tool that takes a path from
// the model may run inside it (see llm-tools).
//
// Every tool also runs with private namespaces: its own /tmp and /dev/shm, and
// a network stack whose only interface is loopback. caged mounts first and
// resolves the Landlock rules after, so the grant of /tmp names the private one.
//
// -disable-sandbox turns it off. A missing caged binary is a startup error,
// never a fallback to unsandboxed execution.
//
// Design notes are in docs/source/sandbox.rst. The policies below are a
// starting point; -sandbox-ro/-sandbox-rw extend them.

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

// toolboxHandshakeTimeout bounds the startup check of the llm-tools binary,
// which answers one request and exits.
const toolboxHandshakeTimeout = 5 * time.Second

// privateTmp is the temporary directory the tools see. It is never the host's
// /tmp: --private-tmp mounts a fresh tmpfs there, or binds -sandbox-tmp over it.
const privateTmp = "/tmp"

// tempEnvNames are the variables that point at the temporary directory: TMPDIR
// is what os.TempDir reads, GOTMPDIR is the go command's own work directory,
// and some programs consult TMP and TEMP after TMPDIR.
var tempEnvNames = []string{"TMPDIR", "GOTMPDIR", "TMP", "TEMP"}

// defaultCurlPorts are the destinations the curl tool may reach when no
// -curl-port is given: HTTPS and HTTP, the two schemes it accepts.
var defaultCurlPorts = []int{443, 80}

// systemReadOnly are the trees a program needs to run at all: binaries, shared
// libraries and system configuration (CAs, resolv.conf, nsswitch.conf, hosts).
var systemReadOnly = []string{"/usr", "/lib", "/lib64", "/bin", "/etc"}

// sandboxConfig holds the sandbox command-line options.
type sandboxConfig struct {
	// disabled turns the whole sandbox off (-disable-sandbox).
	disabled bool
	// cagedBin is the path of the caged binary (empty: $CAGED_BIN, then
	// $PATH).
	cagedBin string
	// ro/rw extend the read-only/read-write lists of the tool policies.
	ro []string
	rw []string
	// curl exposes the curl tool.
	curl bool
	// curlPorts are the TCP ports the curl tool may connect to (default
	// defaultCurlPorts; ignored with curlAnyPort).
	curlPorts []int
	// curlAnyPort lets the curl tool connect to any TCP port.
	curlAnyPort bool
	// curlHosts restricts the curl tool to these hosts (empty means any).
	curlHosts []string
	// toolsBin is the llm-tools binary (empty: next to the bridge, then
	// $PATH). It runs the filesystem tools out of process.
	toolsBin string
	// toolBin is toolsBin as resolved by validate(): absolute, verified and
	// handshake-checked. Empty when the sandbox is off.
	toolBin string
	// tmpDir is the host directory bind mounted over the tools' /tmp
	// (-sandbox-tmp). Empty means a fresh tmpfs: nothing of the host's is
	// visible and nothing written there survives.
	tmpDir string
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
// Next to the bridge comes before $PATH: the helper answers a protocol this
// bridge defines, so the copy that ships with it is the one to run. A
// different build earlier in the PATH would be a different protocol version.
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
// policy. No project directory is granted: the project changes at runtime
// (set_cwd), so the projects root comes in through -sandbox-rw.
func (c *sandboxConfig) toolPaths() (ro, rw []string) {
	home, _ := os.UserHomeDir()
	ro = append([]string{}, systemReadOnly...)
	// Reading a character device needs only read access. /dev/zero is how a
	// tool writes a file of a fixed size: the device hands out zeros, so there
	// is nothing of the host in it.
	ro = append(ro, "/dev/urandom", "/dev/zero")
	if home != "" {
		// KB assets (ONNX model, tokenizer, libonnxruntime).
		ro = append(ro, filepath.Join(home, ".cache", "llm-bridge"))
	}
	ro = append(ro, c.ro...)

	// A child with no stdin gets /dev/null opened for writing, so read access
	// is not enough. /tmp and /dev/shm are the private ones: the policy is
	// built after the mounts.
	rw = []string{"/dev/null", privateTmp, "/dev/shm"}
	if home != "" {
		// Per-project knowledge bases (data.json) and the bridge's config.
		rw = append(rw, filepath.Join(home, ".local", "share", "llm-bridge"))
		// Global context and hooks. The next session reads this directory back
		// into the prompt and execs the scripts in it, so a write here is a
		// high-risk approval (tools.PathRisk).
		rw = append(rw, filepath.Join(home, ".llm-bridge"))
	}
	rw = append(rw, c.rw...)
	return ro, rw
}

// ensureBridgeDir creates ~/.llm-bridge before any policy grants it: caged
// opens every granted path at setup, and a missing one is exit 125.
func (c *sandboxConfig) ensureBridgeDir() error {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	dir := filepath.Join(home, ".llm-bridge")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	return nil
}

// ensureTempDir creates the bind source of -sandbox-tmp before caged mounts it.
// An empty -sandbox-tmp means a fresh tmpfs, with nothing to create.
//
// 0700: a world-writable directory under /tmp would let another user plant
// files or symlinks there.
func (c *sandboxConfig) ensureTempDir() error {
	if c.tmpDir == "" {
		return nil
	}
	if err := os.MkdirAll(c.tmpDir, 0o700); err != nil {
		return fmt.Errorf("create temp directory %s: %w", c.tmpDir, err)
	}
	return nil
}

// tempEnv points every temporary-directory convention at the private /tmp. The
// tools get caged's base env, which has no TMPDIR, so without this a program
// would look somewhere the policy does not grant.
func tempEnv() []string {
	env := make([]string, 0, len(tempEnvNames))
	for _, name := range tempEnvNames {
		env = append(env, name+"="+privateTmp)
	}
	return env
}

// toolSandbox builds the sandbox for the external processes the bridge spawns
// (shell, grep, hooks), or nil when the sandbox is off.
//
// A tool gets no KeepEnv, so caged's base environment only and the API keys
// stay out. Real outbound access belongs to curl alone (see curlSandbox): the
// tools get a loopback-only namespace, where there is nowhere to go.
func (c *sandboxConfig) toolSandbox() (*sandbox.Sandbox, error) {
	if c.disabled {
		return nil, nil
	}
	caged, err := c.binary()
	if err != nil {
		return nil, err
	}
	ro, rw := c.toolPaths()
	// The filesystem tools run as llm-tools, so its directory has to be
	// readable and executable inside this policy.
	if c.toolBin != "" {
		ro = append(ro, filepath.Dir(c.toolBin))
	}
	p := sandbox.Policy{
		ReadOnly:  existing(ro),
		ReadWrite: existing(rw),
		Env:       tempEnv(),
		// A fresh tmpfs for /tmp and /dev/shm, or the -sandbox-tmp bind. The
		// grants above name the private ones.
		PrivateTmp:     true,
		PrivateTmpPath: c.tmpDir,
		PrivateShm:     true,
		// The namespace is the bound: there is nowhere to connect to, and each
		// call gets a fresh one, so a server started by one call is not
		// reachable from the next.
		LoopbackOnly:        true,
		AllowNetworkSockets: true,
		ConnectTCP:          true,
		BindTCP:             true,
	}
	return sandbox.New(caged, p)
}

// curlSandbox builds the sandbox for the curl tool: the system trees and
// nothing else, so a fetch has no local file to read (and therefore nothing to
// leak into a URL, a header or a DNS query). nil means the tool is off.
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
		// The families have to be allowed for any TCP connection to exist; the
		// port rules then narrow where it may go.
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

// requireReportFD checks at startup that bin understands --report-fd, the
// descriptor that tells a failed sandbox from a command that exited with the
// same status. An old caged is a startup error, not a downgrade to guessing.
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

// namespaceProbeTimeout bounds the startup probe. caged's namespace setup is a
// handful of syscalls, so anything slower is not going to work at all.
const namespaceProbeTimeout = 10 * time.Second

// probeNamespaces runs one throwaway command with the namespaces every tool
// gets, so a kernel or an LSM that refuses them stops the bridge at startup.
//
// --help cannot answer this: listing a flag says nothing about whether this
// process can create a namespace. unshare(CLONE_NEWUSER) fails for
// environmental reasons (unprivileged_userns_clone=0, an AppArmor profile, a
// bridge already inside another cage) and the setup has to write
// /proc/self/uid_map. The exit status catches an old caged (exit 2), the report
// descriptor catches a setup that did not apply (exit 125).
func probeNamespaces(bin string) error {
	sb, err := sandbox.New(bin, sandbox.Policy{
		ReadOnly:  existing(systemReadOnly),
		ReadWrite: existing([]string{privateTmp, "/dev/shm"}),
		// The probe exercises the same mounts the tools get, including a
		// grant that names the private /tmp: a Landlock rule applied before
		// the mount would fail here instead of on every tool call.
		PrivateTmp:   true,
		PrivateShm:   true,
		LoopbackOnly: true,
	})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), namespaceProbeTimeout)
	defer cancel()
	if _, err := sb.Run(ctx, "", "/bin/true"); err != nil {
		return fmt.Errorf("caged %s could not build the private namespaces (user, mount and network), which every tool call needs: it takes unshare(CLONE_NEWUSER), a writable /proc/self/uid_map and unshare(CLONE_NEWNS), so a bridge that is itself running inside another cage cannot: %w", bin, err)
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
	// Created before any policy names them, because caged exits 125 on a path
	// it cannot open.
	if err := c.ensureBridgeDir(); err != nil {
		return err
	}
	if err := c.ensureTempDir(); err != nil {
		return err
	}
	// Checked by running it, not by reading --help: see probeNamespaces.
	if err := probeNamespaces(bin); err != nil {
		return err
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
	for _, port := range c.curlPorts {
		if port < 1 || port > 65535 {
			return fmt.Errorf("-curl-port %d out of range 1..65535", port)
		}
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
	flag.StringVar(&c.tmpDir, "sandbox-tmp", "",
		"host directory bind mounted over the tools' private /tmp, to exchange files with them (default: a fresh tmpfs, where nothing written survives and nothing of the host's /tmp is visible)")
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
	return c
}
