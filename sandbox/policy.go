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

// Package sandbox wraps the external caged binary. It turns a Policy into
// caged's argv and builds the exec.Cmd values that run a command under it. A
// sandbox that did not apply comes back as a *SetupError, not as the command's
// output.
//
// Design notes are in docs/source/sandbox.rst.
package sandbox

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Policy is the sandbox caged should build for a command. The zero value
// denies everything: no readable or writable path, no network.
//
// ReadOnly and ReadWrite take directories or single regular files. The paths
// have to exist when caged runs, which opens each one before restricting
// itself: a missing path is exit 125.
type Policy struct {
	// ReadOnly holds paths granted read (and execute/traverse) access.
	ReadOnly []string
	// ReadWrite holds paths granted read+write access. A single file works
	// too.
	ReadWrite []string
	// MountReadOnly holds paths mounted read-only in the command namespace.
	MountReadOnly []string

	// PrivateTmp mounts a fresh tmpfs over /tmp in the command's mount
	// namespace. The filesystem rules are created after the mount, so a
	// ReadWrite grant of /tmp names the private one.
	PrivateTmp bool
	// PrivateTmpPath turns PrivateTmp into a bind mount of this host directory
	// over /tmp, so the host and the command see the same files. The path has
	// to exist; an empty value means a fresh tmpfs.
	PrivateTmpPath string
	// PrivateShm mounts a fresh tmpfs over /dev/shm, in the same mount
	// namespace PrivateTmp uses. It works alone: caged builds the namespace
	// for either flag.
	PrivateShm bool

	// LoopbackOnly gives the command its own network namespace, with loopback
	// up and no route out, so an off-host connect fails with ENETUNREACH.
	// Alone it changes nothing: seccomp still denies socket(AF_INET). Set it
	// with AllowNetworkSockets, ConnectTCP and BindTCP together. See
	// docs/source/sandbox.rst.
	LoopbackOnly bool

	// AllowNetworkSockets releases AF_INET, AF_INET6 and AF_NETLINK, which
	// seccomp denies by default. Without it no TCP socket can be created.
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

	// KeepEnv passes the whole environment through. Without it caged replaces
	// the environment with its base set (PATH, HOME, LANG, LANGUAGE, LC_*).
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

// Validate reports the conflicts and out-of-range values caged would reject
// with a usage error (exit 2), so a broken policy fails at startup with a
// clear message.
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

// Args returns the caged arguments for this policy, without the "--" separator
// and the command, which Command appends. Flags whose value means "unset" are
// omitted.
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
	for _, path := range p.MountReadOnly {
		args = append(args, "--mount-ro", path)
	}
	if p.PrivateTmpPath != "" {
		args = append(args, "--private-tmp", p.PrivateTmpPath)
	} else if p.PrivateTmp {
		args = append(args, "--private-tmp")
	}
	if p.PrivateShm {
		args = append(args, "--private-shm")
	}
	if p.LoopbackOnly {
		args = append(args, "--loopback-only")
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
