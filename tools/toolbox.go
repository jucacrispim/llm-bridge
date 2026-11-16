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

package tools

// Both ends of the llm-tools protocol: one JSON object per line in, one per
// line out, until the input ends.
//
//	{"name":"read","args":{"path":"main.go"}}
//	{"ok":true,"result":"..."}          or  {"ok":false,"error":"..."}
//
// The tool name is part of the call, so there is no flag and no subcommand.
// Design notes are in docs/source/tools.rst.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
)

const (
	// ToolboxVersion is the protocol version the binary speaks. It goes in the
	// handshake, so a stale llm-tools fails at startup.
	ToolboxVersion = "1"

	// toolboxPing is the handshake request. It never reaches the dispatcher.
	toolboxPing = "__ping"

	// toolboxProduct is what the handshake response must start with.
	toolboxProduct = "llm-tools"
)

// toolboxRequest is one tool call.
type toolboxRequest struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// toolboxResponse is the outcome of one tool call: exactly one of Result and
// Error is meaningful, and OK says which.
type toolboxResponse struct {
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

// IsFileTool reports whether name is one of the tools llm-tools serves. shell,
// grep and curl are not here: they exec an external program, so they have to be
// the sandboxed command itself.
func IsFileTool(name string) bool {
	switch name {
	case "read", "write", "glob", "search_replace":
		return true
	}
	return false
}

// Serve runs the llm-tools loop: read a request, run it, write the response,
// until in is exhausted. There is no context because these tools do no blocking
// I/O a cancellation could interrupt.
func Serve(in io.Reader, out io.Writer) error {
	dec := json.NewDecoder(in)
	enc := json.NewEncoder(out)
	exec := NewExecutor("")

	for {
		var req toolboxRequest
		if err := dec.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			// A malformed request leaves the stream in an unknown position:
			// report and stop rather than answer the wrong call.
			return fmt.Errorf("llm-tools: invalid request: %w", err)
		}
		if err := enc.Encode(serveOne(exec, req)); err != nil {
			return err
		}
	}
}

// serveOne runs a single decoded request and returns the response to send.
func serveOne(exec *Executor, req toolboxRequest) toolboxResponse {
	if req.Name == toolboxPing {
		return toolboxResponse{OK: true, Result: toolboxProduct + "/" + ToolboxVersion}
	}
	if !IsFileTool(req.Name) {
		return toolboxResponse{Error: fmt.Sprintf(
			"llm-tools: unsupported tool %q (it serves read, write, glob and search_replace; shell, grep and curl are run by caged directly)",
			req.Name)}
	}
	result, err := exec.Execute(context.Background(), req.Name, string(req.Args))
	if err != nil {
		return toolboxResponse{Error: err.Error()}
	}
	return toolboxResponse{OK: true, Result: result}
}

// toolboxRequestLine renders one call as the JSON line llm-tools reads. A
// malformed payload becomes an empty object, so the tool reports its own
// missing field instead of a parse error about JSON the model just sent.
func toolboxRequestLine(name, rawArgs string) ([]byte, error) {
	args := json.RawMessage(rawArgs)
	if len(bytes.TrimSpace(args)) == 0 || !json.Valid(args) {
		args = json.RawMessage("{}")
	}
	line, err := json.Marshal(toolboxRequest{Name: name, Args: args})
	if err != nil {
		return nil, fmt.Errorf("llm-tools: encode request: %w", err)
	}
	return append(line, '\n'), nil
}

// decodeToolboxResponse turns the helper's stdout into a tool result. Anything
// that is not a well-formed response is an error: an empty or garbled reply
// means the helper did not run, or died.
func decodeToolboxResponse(stdout []byte) (string, error) {
	var resp toolboxResponse
	if err := json.Unmarshal(bytes.TrimSpace(stdout), &resp); err != nil {
		return "", fmt.Errorf("llm-tools: unreadable response (%d bytes of output): %w", len(stdout), err)
	}
	if !resp.OK {
		if resp.Error == "" {
			return "", errors.New("llm-tools: reported a failure with no message")
		}
		return "", errors.New(resp.Error)
	}
	return resp.Result, nil
}

// CheckToolbox runs bin once with the handshake request and verifies that it is
// an llm-tools speaking this protocol version. A missing, stale or foreign
// binary is a startup error, like a caged without --report-fd.
func CheckToolbox(ctx context.Context, bin string) error {
	ping, err := toolboxRequestLine(toolboxPing, "")
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, bin)
	cmd.Stdin = bytes.NewReader(ping)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("llm-tools %s: %w", bin, err)
	}
	var resp toolboxResponse
	if err := json.Unmarshal(bytes.TrimSpace(out), &resp); err != nil {
		return fmt.Errorf("llm-tools %s does not speak the llm-tools protocol: %w", bin, err)
	}
	want := toolboxProduct + "/" + ToolboxVersion
	if !resp.OK || resp.Result != want {
		return fmt.Errorf("llm-tools %s: unexpected handshake %q (want %q); rebuild both binaries together", bin, resp.Result, want)
	}
	return nil
}
