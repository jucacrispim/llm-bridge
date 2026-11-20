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

// MCP servers: local commands that announce tools and run them. The config
// names the binary and what its sandbox grants; the bridge runs it once per
// call, exactly like llm-tools.
//
// Design notes are in .llm-bridge/mcp-design.md and docs/source/mcp.rst.

import (
	"flag"
	"fmt"
	"os/exec"
	"path/filepath"

	"llm-bridge/mcp"
	"llm-bridge/sandbox"
)

// mcpConfig holds the MCP flags and the servers they resolve to.
type mcpConfig struct {
	enabled bool
	path    string
	servers []mcp.Server
}

// registerMCPFlags registers the MCP flags and returns the config they fill.
func registerMCPFlags() *mcpConfig {
	c := &mcpConfig{}
	flag.BoolVar(&c.enabled, "mcp", true,
		"expose the tools of the MCP servers in the config file")
	flag.StringVar(&c.path, "mcp-config", "",
		"path of the MCP server config (default ~/.config/llm-bridge/mcp.json)")
	return c
}

// validate loads the config and builds one server per entry. A server that
// cannot be resolved is an error here, at startup. A server that fails to list
// its tools is only a warning later, in NewManager.
func (c *mcpConfig) validate(sb *sandboxConfig) error {
	if !c.enabled {
		return nil
	}
	cfg, err := mcp.Load(c.path)
	if err != nil {
		return err
	}
	for _, name := range cfg.Names() {
		s := cfg.Servers[name].Normalize()
		bin, err := resolveCommand(s.Command)
		if err != nil {
			return fmt.Errorf("mcp server %s: %w", name, err)
		}
		limits, err := s.Limits()
		if err != nil {
			return fmt.Errorf("mcp server %s: %w", name, err)
		}
		sbox, err := sb.mcpSandbox(s, bin)
		if err != nil {
			return fmt.Errorf("mcp server %s: %w", name, err)
		}
		readOnly := make(map[string]bool, len(s.ReadOnly))
		for _, tool := range s.ReadOnly {
			readOnly[tool] = true
		}
		c.servers = append(c.servers, mcp.Server{
			Name:     name,
			Bin:      bin,
			Args:     s.Args,
			Dir:      s.Dir,
			Limits:   limits,
			ReadOnly: readOnly,
			Sandbox:  sbox,
		})
	}
	return nil
}

// manager lists the tools of every server, concurrently. A server that does
// not answer is left out with a warning.
func (c *mcpConfig) manager() *mcp.Manager {
	if !c.enabled || len(c.servers) == 0 {
		return nil
	}
	servers := make([]*mcp.Server, 0, len(c.servers))
	for i := range c.servers {
		servers = append(servers, &c.servers[i])
	}
	return mcp.NewManager(servers)
}

// resolveCommand turns the configured command into a path, so a binary that
// disappears from $PATH fails at startup rather than on the first call.
func resolveCommand(command string) (string, error) {
	if filepath.IsAbs(command) {
		return command, nil
	}
	found, err := exec.LookPath(command)
	if err != nil {
		return "", fmt.Errorf("command %q not found: %w", command, err)
	}
	return found, nil
}

// mcpSandbox builds the policy of one MCP server: the tool policy plus what the
// server config asks for. nil means the sandbox is off.
//
// The server runs with private namespaces like every other tool, so a server
// that needs a network reach has to say so: with Network off, loopback is the
// only interface.
func (c *sandboxConfig) mcpSandbox(s mcp.ServerConfig, bin string) (*sandbox.Sandbox, error) {
	if c.disabled {
		return nil, nil
	}
	caged, err := c.binary()
	if err != nil {
		return nil, err
	}
	ro, rw := c.toolPaths()
	ro = append(ro, s.RO...)
	rw = append(rw, s.RW...)
	// caged opens every granted path before restricting itself, and execs the
	// binary from inside the policy.
	ro = append(ro, filepath.Dir(bin))
	// Every .git under the RW roots is mounted read-only, so no tool edits
	// hooks or config behind the human's back. A server that runs git needs
	// them writable, and only it gets that: the mount is per server.
	mounts := c.gitMounts
	if s.WritableGit {
		mounts = nil
	}
	p := sandbox.Policy{
		ReadOnly:            existing(ro),
		ReadWrite:           existing(rw),
		MountReadOnly:       mounts,
		Env:                 tempEnv(),
		PrivateTmp:          true,
		PrivateShm:          true,
		AllowNetworkSockets: true,
	}
	if s.Network {
		p.ConnectTCP = s.AnyPort
		if !s.AnyPort {
			p.ConnectPorts = mcpPorts(s.Ports)
		}
		p.BindTCP = true
	} else {
		p.LoopbackOnly = true
		p.ConnectTCP = true
		p.BindTCP = true
	}
	return sandbox.New(caged, p)
}

// mcpPorts defaults to HTTPS: a server that asked for the network without a
// port list should not reach every port.
func mcpPorts(ports []int) []int {
	if len(ports) == 0 {
		return []int{443}
	}
	return ports
}
