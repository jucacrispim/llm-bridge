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

package mcp

// Manager is the set of servers that answered at startup, with the tools they
// announced under the names the model sees.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/sandbox"
)

// Manager holds the tools of every server that answered. The zero value is
// usable and has no tools, which is what a bridge with MCP off gets.
type Manager struct {
	tools map[string]toolRef
	list  []llm.Tool
}

// toolRef is where an exposed name lands: the server and the tool it names.
type toolRef struct {
	server *Server
	name   string
}

// Empty returns a manager with no servers.
func Empty() *Manager {
	return &Manager{}
}

// NewManager lists the tools of every server, concurrently, and keeps those
// that answered. A server that fails to list is a warning, never a fatal
// error: a broken MCP server must not keep the bridge from starting.
func NewManager(servers []*Server) *Manager {
	m := &Manager{tools: map[string]toolRef{}}
	type result struct {
		server *Server
		tools  []Tool
		err    error
	}
	results := make(chan result, len(servers))
	var wg sync.WaitGroup
	for _, srv := range servers {
		wg.Add(1)
		go func(s *Server) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
			defer cancel()
			tools, err := s.List(ctx)
			results <- result{server: s, tools: tools, err: err}
		}(srv)
	}
	wg.Wait()
	close(results)
	for r := range results {
		if r.err != nil {
			logger.Warningf("mcp server %s disabled: %v", r.server.Name, r.err)
			continue
		}
		kept := 0
		for _, t := range r.tools {
			exposed := exposedName(r.server.Name, t.Name)
			// Two server names may sanitize to the same text, and one server
			// may repeat a tool: the first one wins, the rest would be a
			// different call under the same name.
			if _, dup := m.tools[exposed]; dup {
				logger.Warningf("mcp server %s: tool %q skipped, %s is already taken", r.server.Name, t.Name, exposed)
				continue
			}
			m.tools[exposed] = toolRef{server: r.server, name: t.Name}
			m.list = append(m.list, llm.Tool{
				Name:        exposed,
				Description: t.Description,
				Parameters:  t.Parameters,
				ReadOnly:    t.ReadOnly,
			})
			kept++
		}
		logger.Infof("mcp server %s: %d tool(s)", r.server.Name, kept)
	}
	// The listing arrives in completion order; the tool list is sorted so the
	// prompt and the prefix cache do not depend on which server answered first.
	sort.Slice(m.list, func(i, j int) bool { return m.list[i].Name < m.list[j].Name })
	return m
}

// Tools returns the tools to offer the model.
func (m *Manager) Tools() []llm.Tool {
	if m == nil {
		return nil
	}
	return m.list
}

// Has reports whether name is a tool of a configured server.
func (m *Manager) Has(name string) bool {
	if m == nil {
		return false
	}
	_, ok := m.tools[name]
	return ok
}

// Mutates reports whether the call has to be confirmed. Only the tools the
// config lists as read-only run inline; the server's own hint is not a
// decision the bridge can take.
func (m *Manager) Mutates(name string) bool {
	ref, ok := m.tools[name]
	if !ok {
		return true
	}
	return !ref.server.ReadOnly[ref.name]
}

// Call runs a tool and returns its text. The fragments go to onText as they
// arrive; the result is what the history gets.
//
// A stream cut by a limit is not a failure: the text carries a line saying why
// it stopped, and the turn moves on.
func (m *Manager) Call(ctx context.Context, exposed string, args json.RawMessage, onText func(string) error) (string, error) {
	ref, ok := m.tools[exposed]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", exposed)
	}
	var b strings.Builder
	emit := func(text string) error {
		b.WriteString(text)
		if onText == nil {
			return nil
		}
		return onText(text)
	}
	err := ref.server.Call(ctx, ref.name, args, emit)
	text := b.String()
	switch {
	case err == nil:
		return text, nil
	case errors.Is(err, sandbox.ErrStreamIdle):
		return stopped(text, "stopped after going idle"), nil
	case errors.Is(err, sandbox.ErrStreamTotal):
		return stopped(text, "stopped at the total deadline"), nil
	case errors.Is(err, sandbox.ErrStreamBytes):
		return stopped(text, "output truncated at the byte limit"), nil
	case errors.Is(err, ErrToolError):
		if strings.TrimSpace(text) == "" {
			text = "the tool reported an error"
		}
		return "error: " + text, nil
	}
	return text, err
}

// stopped appends the reason a stream ended early to what it produced.
func stopped(text, reason string) string {
	line := "[mcp: " + reason + "]"
	if text == "" {
		return line
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + line
}
