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

// Package mcp exposes the tools of local commands as bridge tools.
//
// A server is a command that reads one JSON request from stdin and prints JSON
// values on stdout: {"method":"tools/list"} answers with the tools,
// {"method":"tools/call"} with the result, which may come in pieces. The
// bridge runs it once per call, under the sandbox policy the configuration
// asks for.
//
// Design notes are in .llm-bridge/mcp-design.md; the user-facing description
// is in docs/source/mcp.rst.
package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"llm-bridge/sandbox"
)

const (
	// configDir and configFile place the user config under os.UserConfigDir.
	configDir  = "llm-bridge"
	configFile = "mcp.json"

	// listTimeout bounds the tool listing of one server at startup. A server
	// that does not answer costs this much, not the whole boot.
	listTimeout = 10 * time.Second
)

// Config is the parsed configuration file. A missing file means no servers.
type Config struct {
	Servers map[string]ServerConfig `json:"servers"`
}

// ServerConfig is one server as written in the file. The paths are relative to
// nothing in particular: a leading ~ is expanded, everything else is taken as
// written.
type ServerConfig struct {
	// Command is the binary to run, either absolute or found in $PATH.
	Command string `json:"command"`
	// Args are passed before nothing; the request goes on stdin.
	Args []string `json:"args,omitempty"`
	// Dir is the working directory of the server.
	Dir string `json:"cwd,omitempty"`
	// RO and RW extend the sandbox of this server. The base policy is the same
	// the shell tool gets.
	RO []string `json:"ro,omitempty"`
	RW []string `json:"rw,omitempty"`
	// Network lets the server reach hosts other than loopback. Off means the
	// network namespace has nowhere to go, like every other tool.
	Network bool `json:"network,omitempty"`
	// Ports are the destinations allowed with Network (default 443).
	Ports []int `json:"ports,omitempty"`
	// AnyPort lifts the port restriction, for a server that talks on many.
	AnyPort bool `json:"any_port,omitempty"`
	// Idle and Total bound one call, as Go durations ("20s", "5m").
	Idle  string `json:"idle,omitempty"`
	Total string `json:"total,omitempty"`
	// ReadOnly lists the tools that run without confirmation. Everything else
	// is mutating, because a tool the server merely hints as read-only is not a
	// decision the bridge can take.
	ReadOnly []string `json:"read_only,omitempty"`
}

// DefaultPath returns the user config path, or "" when there is no config dir.
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil || dir == "" {
		return ""
	}
	return filepath.Join(dir, configDir, configFile)
}

// Load reads the config at path, or the default path when it is empty. A
// missing file is an empty config, not an error: the bridge works without MCP.
func Load(path string) (*Config, error) {
	if path == "" {
		path = DefaultPath()
	}
	if path == "" {
		return &Config{}, nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &Config{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &cfg, nil
}

// Names returns the server names in a stable order.
func (c *Config) Names() []string {
	names := make([]string, 0, len(c.Servers))
	for name := range c.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// validate expands the paths and rejects what the bridge cannot run.
func (c *Config) validate() error {
	for name, s := range c.Servers {
		if !validName.MatchString(name) {
			return fmt.Errorf("server %q: the name may hold letters, digits, underscore and hyphen only", name)
		}
		if s.Command == "" {
			return fmt.Errorf("server %s: missing command", name)
		}
	}
	return nil
}

// Normalize expands the paths of one entry. It runs after the config is read,
// so the values are ready to be used as they are.
func (s ServerConfig) Normalize() ServerConfig {
	s.Command = expandPath(s.Command)
	s.Dir = expandPath(s.Dir)
	s.RO = expandAll(s.RO)
	s.RW = expandAll(s.RW)
	return s
}

// Limits parses the call bounds. An unparsable duration is an error, so a
// typo in the config shows up at startup.
func (s ServerConfig) Limits() (sandbox.StreamLimits, error) {
	var l sandbox.StreamLimits
	if s.Idle != "" {
		d, err := time.ParseDuration(s.Idle)
		if err != nil {
			return l, fmt.Errorf("invalid idle %q: %w", s.Idle, err)
		}
		l.Idle = d
	}
	if s.Total != "" {
		d, err := time.ParseDuration(s.Total)
		if err != nil {
			return l, fmt.Errorf("invalid total %q: %w", s.Total, err)
		}
		l.Total = d
	}
	return l, nil
}

// ReadOnlyTool reports whether the named tool runs without confirmation.
func (s ServerConfig) ReadOnlyTool(tool string) bool {
	for _, name := range s.ReadOnly {
		if name == tool {
			return true
		}
	}
	return false
}

// expandPath expands a leading ~ and leaves everything else alone.
func expandPath(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		if path == "~" {
			return home
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// expandAll applies expandPath to a list.
func expandAll(paths []string) []string {
	if paths == nil {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = expandPath(p)
	}
	return out
}
