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

// One server, as it runs: list the tools once at startup, call one per request.
// Both go through sandbox.RunStream, so the process gets the same limits and
// the same kill-on-cancel a shell call gets.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"llm-bridge/sandbox"
)

// ErrToolError means the server marked the result as an error. The text has
// already been delivered to onText.
var ErrToolError = errors.New("mcp: the tool reported an error")

// Server is one configured command, ready to run.
type Server struct {
	Name string
	// Bin is the command to run, absolute or found in $PATH.
	Bin string
	// Args go before nothing: the request travels on stdin.
	Args []string
	Dir  string
	// Limits bound one call or one listing.
	Limits sandbox.StreamLimits
	// ReadOnly names the tools that run without confirmation.
	ReadOnly map[string]bool
	// Sandbox runs the command. Nil means unsandboxed, which is what
	// -disable-sandbox gives.
	Sandbox *sandbox.Sandbox
}

// Tool is one tool a server announced.
type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	ReadOnly    bool
}

// toolWire is a tool as tools/list prints it.
type toolWire struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	ReadOnlyHint bool            `json:"readOnlyHint"`
}

// contentPart is one piece of an MCP result.
type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// chunk is one value of a tool call: the plain shape (text) or the MCP one
// (content[]). Both may appear in the same stream.
type chunk struct {
	Text    string        `json:"text"`
	Content []contentPart `json:"content"`
	IsError bool          `json:"isError"`
}

// List runs tools/list and returns the tools the server announced.
func (s *Server) List(ctx context.Context) ([]Tool, error) {
	var listed []toolWire
	req := []byte(`{"method":"tools/list"}` + "\n")
	err := s.run(ctx, s.listLimits(), req, func(raw json.RawMessage) error {
		var resp struct {
			Tools *[]toolWire `json:"tools"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return fmt.Errorf("mcp %s: unreadable tools/list answer: %w", s.Name, err)
		}
		if resp.Tools == nil {
			return fmt.Errorf("mcp %s: tools/list answered without a tools list", s.Name)
		}
		listed = *resp.Tools
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := make([]Tool, 0, len(listed))
	for _, t := range listed {
		if t.Name == "" {
			continue
		}
		out = append(out, Tool{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  schemaOrDefault(t.InputSchema),
			ReadOnly:    s.ReadOnly[t.Name],
		})
	}
	return out, nil
}

// Call runs tools/call for name and hands every text fragment to onText. The
// text is delivered as it arrives; the return value is the outcome of the
// whole call, not of the last fragment.
func (s *Server) Call(ctx context.Context, name string, args json.RawMessage, onText func(string) error) error {
	args, err := callArguments(args)
	if err != nil {
		return err
	}
	params := struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}{Name: name, Arguments: args}
	// Marshal cannot fail here: the fields are a string and JSON that was
	// checked just above.
	req, _ := json.Marshal(map[string]any{"method": "tools/call", "params": params})
	failed := false
	err = s.run(ctx, s.Limits, append(req, '\n'), func(raw json.RawMessage) error {
		var c chunk
		if err := json.Unmarshal(raw, &c); err != nil {
			// Not the object shape: whatever the value is, its text is the
			// result. A bare string arrives quoted, so unquote it first.
			var text string
			if json.Unmarshal(raw, &text) == nil {
				return emitText(text, onText)
			}
			return emitText(string(raw), onText)
		}
		if c.IsError {
			failed = true
		}
		for _, part := range c.Content {
			if part.Text == "" {
				continue
			}
			if err := emitText(part.Text, onText); err != nil {
				return err
			}
		}
		return emitText(c.Text, onText)
	})
	if err != nil {
		return err
	}
	if failed {
		return ErrToolError
	}
	return nil
}

// emitText hands one fragment to the caller, if there is one.
func emitText(text string, onText func(string) error) error {
	if text == "" || onText == nil {
		return nil
	}
	return onText(text)
}

// run executes the command with stdin and feeds each printed value to onValue.
func (s *Server) run(ctx context.Context, limits sandbox.StreamLimits, stdin []byte, onValue func(json.RawMessage) error) error {
	if s.Sandbox != nil {
		return s.Sandbox.RunStream(ctx, s.Dir, s.Bin, stdin, limits, onValue, s.Args...)
	}
	cmd := exec.CommandContext(ctx, s.Bin, s.Args...)
	if s.Dir != "" {
		cmd.Dir = s.Dir
	}
	return sandbox.Stream(ctx, cmd, stdin, limits, onValue)
}

// listLimits bounds the startup listing: a server that hangs there must not
// hold the boot for the whole call budget.
func (s *Server) listLimits() sandbox.StreamLimits {
	l := s.Limits
	if l.Total <= 0 || l.Total > listTimeout {
		l.Total = listTimeout
	}
	return l
}

// schemaOrDefault keeps a server from announcing a tool without a schema,
// which the providers reject.
func schemaOrDefault(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 || !json.Valid(raw) {
		return json.RawMessage(`{"type":"object"}`)
	}
	return raw
}

// callArguments keeps a missing payload as an empty object and rejects one that
// is not JSON, so the model gets the parse error instead of the server.
func callArguments(raw json.RawMessage) (json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return json.RawMessage("{}"), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("mcp: the arguments must be valid JSON")
	}
	return raw, nil
}
