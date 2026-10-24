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

// Package sandbox wraps the external `caged` binary. It has two jobs:
//
//   - describe a sandbox policy and turn it into caged's argv (Policy);
//   - build exec.Cmd values that run a command under that policy, and classify
//     the caged exit codes that mean "the sandbox did not apply" as hard
//     errors instead of command output (Sandbox, SetupError).
//
// Landlock stacks by intersection, so a process can be wrapped twice: the
// bridge itself runs under a loose ceiling and every tool it executes runs
// under a stricter policy. Nothing here needs to know about the ceiling: the
// kernel does the intersection.
//
// This package is a thin argv builder on purpose. caged is a separate binary
// with its own CLI contract; keeping the binding here (instead of a separate
// module) means a caged flag rename is a single commit, not a version matrix.
package sandbox

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Policy is the sandbox caged should build for a command. The zero value is
// "deny everything": no readable path, no writable path, no network.
//
// The readonly/readwrite lists accept directories or single regular files
// (caged grants only that file when given a file). Keep in mind that the paths
// must exist when caged runs: caged opens each of them with O_PATH before
// restricting itself, so a missing path makes caged fail with exit 125.
type Policy struct {
	// ReadOnly holds paths granted read (and execute/traverse) access.
	ReadOnly []string
	// ReadWrite holds paths granted read+write access. A single file is
	// enough, e.g. the bridge log file; a directory is never needed for it.
	ReadWrite []string

	// AllowNetworkSockets releases the socket families AF_INET, AF_INET6 and
	// AF_NETLINK (denied by seccomp otherwise). Without it a process cannot
	// create a TCP socket at all, no matter what the Landlock rules say.
	AllowNetworkSockets bool
	// AllowUnixSockets releases AF_UNIX sockets (denied by seccomp by
	// default). The bridge talks over stdio pipes, so it does not need it.
	AllowUnixSockets bool

	// ConnectTCP allows any outgoing TCP connection (mutually exclusive with
	// ConnectPorts).
	ConnectTCP bool
	// ConnectPorts allows outgoing TCP connections to the given ports only
	// (port 0 = ephemeral range). Requires AllowNetworkSockets to be useful.
	ConnectPorts []int
	// BindTCP allows binding any TCP port (mutually exclusive with
	// BindPorts).
	BindTCP bool
	// BindPorts allows binding the given TCP ports only.
	BindPorts []int

	// KeepEnv inherits the whole environment. Without it caged replaces the
	// environment with its small base set (PATH, HOME, LANG, LANGUAGE,
	// LC_*), which is what we want for tools: API keys must not leak into
	// them even when the bridge itself was started with --keep-env.
	KeepEnv bool
	// Env holds extra NAME=VALUE entries to set in the command environment.
	Env []string

	// MaxProcs caps RLIMIT_NPROC. Zero means "do not emit the flag" (caged
	// rejects the value 0).
	MaxProcs int
	// RequireABI turns "this kernel is older than the feature" from a warning
	// into a failure, so the policy is never silently weaker than asked for.
	// Zero means "do not emit the flag" (caged rejects the value 0).
	RequireABI int
}

// Validate reports problems that caged would reject with a usage error (exit
// 2). Catching them here turns an opaque "exit 2" into a clear message, and
// keeps a broken policy from being indistinguishable from a sandbox failure.
func (p Policy) Validate() error {
	if p.ConnectTCP && len(p.ConnectPorts) > 0 {
		return errors.New("sandbox: connect-tcp conflicts with connect ports")
	}
	if p.BindTCP && len(p.BindPorts) > 0 {
		return errors.New("sandbox: bind-tcp conflicts with bind ports")
	}
	for _, port := range p.ConnectPorts {
		if port < 0 || port > 65535 {
			return fmt.Errorf("sandbox: connect port %d out of range 0..65535", port)
		}
	}
	for _, port := range p.BindPorts {
		if port < 0 || port > 65535 {
			return fmt.Errorf("sandbox: bind port %d out of range 0..65535", port)
		}
	}
	for _, kv := range p.Env {
		if !strings.Contains(kv, "=") {
			return fmt.Errorf("sandbox: env entry %q is not NAME=VALUE", kv)
		}
	}
	if p.MaxProcs < 0 {
		return fmt.Errorf("sandbox: max-procs must not be negative (got %d)", p.MaxProcs)
	}
	if p.RequireABI < 0 {
		return fmt.Errorf("sandbox: require-abi must not be negative (got %d)", p.RequireABI)
	}
	return nil
}

// Args returns the caged arguments that describe this policy, without the "--"
// separator and the command (Command appends both). Flags whose value means
// "unset" are omitted.
func (p Policy) Args() []string {
	// nil when nothing is set (not an empty slice): "no flags" reads better
	// and keeps the zero policy comparable.
	var args []string
	for _, path := range p.ReadOnly {
		args = append(args, "--ro", path)
	}
	for _, path := range p.ReadWrite {
		args = append(args, "--rw", path)
	}
	if p.AllowNetworkSockets {
		args = append(args, "--allow-network-sockets")
	}
	if p.AllowUnixSockets {
		args = append(args, "--allow-unix-sockets")
	}
	if p.ConnectTCP {
		args = append(args, "--allow-connect-tcp")
	}
	for _, port := range p.ConnectPorts {
		args = append(args, "--allow-connect-port", strconv.Itoa(port))
	}
	if p.BindTCP {
		args = append(args, "--allow-bind-tcp")
	}
	for _, port := range p.BindPorts {
		args = append(args, "--allow-bind-port", strconv.Itoa(port))
	}
	if p.KeepEnv {
		args = append(args, "--keep-env")
	}
	for _, kv := range p.Env {
		args = append(args, "--env", kv)
	}
	if p.MaxProcs > 0 {
		args = append(args, "--max-procs", strconv.Itoa(p.MaxProcs))
	}
	if p.RequireABI > 0 {
		args = append(args, "--require-abi", strconv.Itoa(p.RequireABI))
	}
	return args
}
