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

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Executor runs the bridge's tools inside the bridge process, against a working
// directory (the current project's cwd, set by the client via `set_cwd`). It
// mirrors the semantics of the tools that used to run in the Emacs client, so
// behavior is unchanged after moving execution into the bridge.
type Executor struct {
	cwd string
}

// NewExecutor returns an Executor rooted at cwd. A relative path argument to
// any tool is resolved against cwd (when cwd is empty, relative paths are left
// as-is and resolved by the process working directory).
func NewExecutor(cwd string) *Executor {
	return &Executor{cwd: cwd}
}

// Execute runs the named tool with raw JSON arguments and returns its textual
// result. The provided context cancels a running external process (shell/grep)
// when the turn is cancelled. On error the caller is expected to surface the
// error text as the tool result (the model still gets a response per call).
func (e *Executor) Execute(ctx context.Context, name, rawArgs string) (string, error) {
	args := map[string]any{}
	if rawArgs != "" {
		// A malformed argument payload is tolerated: the individual tool
		// then reports its own "missing" error, mirroring the client.
		_ = json.Unmarshal([]byte(rawArgs), &args)
	}
	switch name {
	case "read":
		return e.read(args)
	case "write":
		return e.write(args)
	case "shell":
		return e.shell(ctx, args)
	case "grep":
		return e.grep(ctx, args)
	case "glob":
		return e.glob(args)
	case "search_replace":
		return e.searchReplace(args)
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// resolve maps a possibly-relative tool path against the executor's cwd.
func (e *Executor) resolve(path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) || e.cwd == "" {
		return path
	}
	return filepath.Join(e.cwd, path)
}

// expandHome expands a leading "~" (or "~/") in path to the user's home dir,
// mirroring Emacs' `file-expand-wildcards' `~' handling.
func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// number extracts an integer JSON number from a decoded JSON value, reporting
// whether it was present and numeric. Non-numeric values (e.g. a string) are
// treated as absent, matching the client's `numberp` checks.
func number(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// read implements the `read` tool: return the file contents, optionally sliced
// by a 0-based OFFSET (line index) and a LIMIT (max lines).
func (e *Executor) read(args map[string]any) (string, error) {
	path, _ := args["path"].(string)
	if path == "" {
		return "", fmt.Errorf("Read: missing 'path'")
	}
	data, err := os.ReadFile(e.resolve(path))
	if err != nil {
		return "", err
	}
	content := string(data)
	offset, hasOffset := number(args["offset"])
	limit, hasLimit := number(args["limit"])
	if !hasOffset && !hasLimit {
		return content, nil
	}
	lines := strings.Split(content, "\n")
	nlines := len(lines)
	start := 0
	if hasOffset {
		if offset < 0 {
			offset = 0
		}
		if offset > nlines {
			offset = nlines
		}
		start = offset
	}
	end := nlines
	if hasLimit {
		end = start + limit
		if end > nlines {
			end = nlines
		}
		if end < start {
			end = start
		}
	}
	return strings.Join(lines[start:end], "\n"), nil
}

// write implements the `write` tool: write CONTENT to the file at PATH.
func (e *Executor) write(args map[string]any) (string, error) {
	path, okPath := args["path"].(string)
	content, okContent := args["content"].(string)
	if !okPath || !okContent {
		return "", fmt.Errorf("Write: missing 'path' or 'content'")
	}
	full := e.resolve(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		return "", err
	}
	return "ok", nil
}

// shell implements the `shell` tool: run COMMAND via `bash -c`, capturing
// stdout+stderr. A non-zero exit appends a "[exited with status N]" line, like
// the client.
func (e *Executor) shell(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("Shell: missing 'command'")
	}
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	if e.cwd != "" {
		cmd.Dir = e.cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return string(out) + fmt.Sprintf("\n[exited with status %d]", ee.ExitCode()), nil
		}
		return "", err
	}
	return string(out), nil
}

// grep implements the `grep` tool: search PATTERN (a POSIX ERE) under PATH
// (default "."), returning `file:line:text` matches. Exit status 0 (matches)
// and 1 (no matches) are both success, mirroring the client.
func (e *Executor) grep(ctx context.Context, args map[string]any) (string, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("Grep: missing 'pattern'")
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	cmd := exec.CommandContext(ctx, "grep", "-rnEI", "--include=*", pattern, e.resolve(path))
	if e.cwd != "" {
		cmd.Dir = e.cwd
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if _, ok := err.(*exec.ExitError); ok {
			// Any grep exit status is surfaced as output: 0 matches / 1 no
			// matches are normal results, and on error the model still sees
			// whatever grep printed (mirroring the client).
			return string(out), nil
		}
		return "", err
	}
	return string(out), nil
}

// glob implements the `glob` tool, emulating Emacs' `file-expand-wildcards`:
// PATTERN is expanded under PATH (default the cwd), with a leading `~`
// expanded to the home directory. Relative patterns yield relative matches.
func (e *Executor) glob(args map[string]any) (string, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("Glob: missing 'pattern'")
	}
	base, _ := args["path"].(string)
	if base == "" {
		base = e.cwd
	}
	base = expandHome(base)
	pattern = expandHome(pattern)

	full := pattern
	if !filepath.IsAbs(pattern) {
		full = filepath.Join(base, pattern)
	}
	matches, err := filepath.Glob(full)
	if err != nil {
		return "", err
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if !filepath.IsAbs(pattern) && base != "" {
			if rel, err := filepath.Rel(base, m); err == nil {
				m = rel
			}
		}
		out = append(out, m)
	}
	return strings.Join(out, "\n"), nil
}

// searchReplace implements the `search_replace` tool: replace the first exact
// occurrence of SEARCH in PATH with REPLACE (literal, not a regex).
func (e *Executor) searchReplace(args map[string]any) (string, error) {
	path, okPath := args["path"].(string)
	search, okSearch := args["search"].(string)
	replace, okReplace := args["replace"].(string)
	if !okPath || !okSearch || !okReplace {
		return "", fmt.Errorf("Search_replace: missing 'path', 'search' or 'replace'")
	}
	full := e.resolve(path)
	data, err := os.ReadFile(full)
	if err != nil {
		return "", err
	}
	content := string(data)
	idx := strings.Index(content, search)
	if idx < 0 {
		return "", fmt.Errorf("string %q not found in %s", search, path)
	}
	updated := content[:idx] + replace + content[idx+len(search):]
	if err := os.WriteFile(full, []byte(updated), 0o644); err != nil {
		return "", err
	}
	return "ok", nil
}
