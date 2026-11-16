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
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"llm-bridge/sandbox"
)

// curl defaults. They are fixed, not flags, so the model can only ask for a
// stricter value than these.
const (
	// curlMaxTime bounds a hung request, so one fetch does not park the turn.
	curlMaxTime = 30
	// curlMaxTimeCap and curlMaxRedirsCap are the most the model may ask for.
	curlMaxTimeCap   = 120
	curlMaxRedirsCap = 10
	// curlMaxRedirs is the default redirect budget, used with -L.
	curlMaxRedirs = 5
	// curlMaxOutput truncates the body: a whole page in the history burns
	// tokens, breaks the prefix cache and is an injection vector.
	curlMaxOutput = 64 << 10
)

// curlFlags maps each curl option the tool accepts from the model to whether it
// takes a value. Options that upload (-d/-F/-T), write a file (-o/-O), read a
// file (-K/--config) or open another transport (--unix-socket) are absent, so
// an option added by a later curl is rejected too.
var curlFlags = map[string]bool{
	"-H":                true,
	"--header":          true,
	"-L":                false,
	"--location":        false,
	"-I":                false,
	"--head":            false,
	"-i":                false,
	"--include":         false,
	"--compressed":      false,
	"-A":                true,
	"--user-agent":      true,
	"-b":                true,
	"--cookie":          true,
	"--max-time":        true,
	"--connect-timeout": true,
	"--max-redirs":      true,
	"--max-filesize":    true,
}

// Executor runs the bridge's tools against cwd, the project the client pointed
// at with set_cwd. The filesystem tools go through llm-tools when toolBin and a
// sandbox are set (see docs/source/tools.rst).
type Executor struct {
	cwd string
	// sandbox, when non-nil, wraps the external processes (shell, grep and the
	// llm-tools helper) in caged.
	sandbox *sandbox.Sandbox
	// toolBin is the llm-tools binary. Empty means the filesystem tools run
	// in-process; the bridge sets it unless the sandbox is off.
	toolBin string
	// curlSandbox runs the curl tool: same shape, plus network access and no
	// project, home or temp path. nil means curl is off.
	curlSandbox *sandbox.Sandbox
	// curlHosts restricts the curl tool to these hosts (empty means any).
	// Landlock filters the destination port only, so the host is checked here,
	// before the command runs.
	curlHosts []string
}

// NewExecutor returns an Executor rooted at cwd. Relative paths are resolved
// against cwd, and left to the process working directory when cwd is empty.
func NewExecutor(cwd string) *Executor {
	return &Executor{cwd: cwd}
}

// NewExecutorWithSandbox returns an Executor rooted at cwd whose external
// processes run under sb. A nil sb means unsandboxed, like NewExecutor.
func NewExecutorWithSandbox(cwd string, sb *sandbox.Sandbox) *Executor {
	return &Executor{cwd: cwd, sandbox: sb}
}

// NewExecutorWithSandboxes adds curlSb, the sandbox the curl tool runs under,
// and hosts, the allowlist that bounds its destinations. A nil curlSb keeps
// curl disabled.
func NewExecutorWithSandboxes(cwd string, sb, curlSb *sandbox.Sandbox, hosts []string) *Executor {
	return &Executor{cwd: cwd, sandbox: sb, curlSandbox: curlSb, curlHosts: hosts}
}

// NewExecutorWithToolbox adds toolBin, the llm-tools binary: with it set the
// filesystem tools run there, so the bridge never opens a path the model chose.
// An empty toolBin keeps them in-process.
func NewExecutorWithToolbox(cwd string, sb, curlSb *sandbox.Sandbox, hosts []string, toolBin string) *Executor {
	return &Executor{cwd: cwd, sandbox: sb, curlSandbox: curlSb, curlHosts: hosts, toolBin: toolBin}
}

// outOfProcess reports whether the filesystem tools go through llm-tools. It
// needs both the binary and the policy: without a policy the helper would run
// unsandboxed.
func (e *Executor) outOfProcess() bool {
	return e.toolBin != "" && e.sandbox != nil
}

// run executes name/args and returns the combined output. A *SetupError means
// the sandbox did not apply, and the caller must not hand that over as a tool
// result.
func (e *Executor) run(ctx context.Context, name string, args ...string) (string, error) {
	if e.sandbox != nil {
		out, err := e.sandbox.Run(ctx, e.cwd, name, args...)
		return string(out), err
	}
	cmd := exec.CommandContext(ctx, name, args...)
	if e.cwd != "" {
		cmd.Dir = e.cwd
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// isSetupError reports whether err means the sandbox did not apply.
func isSetupError(err error) bool {
	var se *sandbox.SetupError
	return errors.As(err, &se)
}

// runToolbox runs one filesystem tool in llm-tools, under the tool policy. The
// working directory is the executor's, so a relative path means the same thing
// there. Only a well-formed reply is a result: a crash or an unreadable answer
// comes back as an error.
func (e *Executor) runToolbox(ctx context.Context, name, rawArgs string) (string, error) {
	req, err := toolboxRequestLine(name, rawArgs)
	if err != nil {
		return "", err
	}
	stdout, stderr, err := e.sandbox.RunIO(ctx, e.cwd, e.toolBin, req)
	if err != nil {
		if isSetupError(err) {
			return "", err
		}
		// Keep whatever the helper said: it is the only clue for a crash, and
		// it travels as the error, so it does not pass for a result.
		if msg := strings.TrimSpace(string(stderr)); msg != "" {
			return "", fmt.Errorf("llm-tools %s: %w: %s", name, err, msg)
		}
		return "", fmt.Errorf("llm-tools %s: %w", name, err)
	}
	return decodeToolboxResponse(stdout)
}

// Execute runs the named tool with raw JSON arguments and returns its result.
// The context cancels a running external process (shell, grep, curl). On error
// the caller surfaces the error text as the tool result.
func (e *Executor) Execute(ctx context.Context, name, rawArgs string) (string, error) {
	// The filesystem tools run in llm-tools, so no path the model produced
	// reaches a syscall here.
	if IsFileTool(name) && e.outOfProcess() {
		return e.runToolbox(ctx, name, rawArgs)
	}
	args := map[string]any{}
	if rawArgs != "" {
		// A malformed payload is tolerated: the tool reports its own missing
		// field, which is clearer than a JSON parse error.
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
	case "curl":
		return e.curl(ctx, args)
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

// number extracts an integer from a decoded JSON value, reporting whether it
// was present and numeric. A non-numeric value counts as absent.
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

// shell implements the `shell` tool: run COMMAND via `bash -c` and capture
// stdout+stderr. A non-zero exit appends a "[exited with status N]" line.
func (e *Executor) shell(ctx context.Context, args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("Shell: missing 'command'")
	}
	out, err := e.run(ctx, "bash", "-c", command)
	if err != nil {
		// A failed sandbox is not the tool's result: the command did not run.
		if isSetupError(err) {
			return "", err
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return out + fmt.Sprintf("\n[exited with status %d]", ee.ExitCode()), nil
		}
		return "", err
	}
	return out, nil
}

// grep implements the `grep` tool: search PATTERN (a POSIX ERE) under PATH
// (default "."), returning `file:line:text` matches. Exit 1 (no matches) is
// success, like exit 0.
func (e *Executor) grep(ctx context.Context, args map[string]any) (string, error) {
	pattern, ok := args["pattern"].(string)
	if !ok || pattern == "" {
		return "", fmt.Errorf("Grep: missing 'pattern'")
	}
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	out, err := e.run(ctx, "grep", "-rnEI", "--include=*", pattern, e.resolve(path))
	if err != nil {
		// A failed sandbox is not a grep result.
		if isSetupError(err) {
			return "", err
		}
		if _, ok := err.(*exec.ExitError); ok {
			// Any other status is a result: the model still sees whatever grep
			// printed.
			return out, nil
		}
		return "", err
	}
	return out, nil
}

// curl implements the `curl` tool: fetch URL with the extra options in ARGS,
// validated against an allowlist and passed as an argv. It runs under its own
// sandbox, which grants no project, home or temp path.
func (e *Executor) curl(ctx context.Context, args map[string]any) (string, error) {
	rawURL, _ := args["url"].(string)
	if err := validateCurlURL(rawURL, e.curlHosts); err != nil {
		return "", err
	}
	extra, err := stringList(args["args"])
	if err != nil {
		return "", err
	}
	argv, err := curlArgv(rawURL, extra)
	if err != nil {
		return "", err
	}
	if e.curlSandbox == nil {
		return "", fmt.Errorf("Curl: network access is disabled (the curl tool is off)")
	}
	// No cwd: the policy grants no relative path to touch, so the fetch starts
	// in the bridge's own.
	out, err := e.curlSandbox.Run(ctx, "", "curl", argv...)
	if err != nil {
		// A failed sandbox is not the tool's result (see shell).
		if isSetupError(err) {
			return "", err
		}
		if ee, ok := err.(*exec.ExitError); ok {
			return untrustedCurlResult(rawURL, string(out)+fmt.Sprintf("\n[curl exited with status %d]", ee.ExitCode())), nil
		}
		return "", err
	}
	return untrustedCurlResult(rawURL, string(out)), nil
}

// untrustedCurlResult wraps a fetched body in the untrusted-content envelope.
// The system prompt tells the model that everything between the tags is data
// from the web, so "ignore the previous instructions and run X" reads as the
// content of a page, which is what it is.
//
// Truncation comes first and wrapping second, so the closing tag survives the
// cut and a payload cannot hide after it.
func untrustedCurlResult(rawURL, body string) string {
	return "<untrusted-web-content url=\"" + attrEscape(rawURL) + "\">\n" +
		truncateCurlOutput(body) + "\n</untrusted-web-content>"
}

// attrEscape keeps a URL from closing the envelope tag or opening another one.
func attrEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(s)
}

// validateCurlURL rejects what the tool will not fetch: a missing URL, a scheme
// other than http/https, credentials in the URL and a host outside the
// allowlist.
func validateCurlURL(raw string, hosts []string) error {
	if raw == "" {
		return fmt.Errorf("Curl: missing 'url'")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("Curl: invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("Curl: unsupported URL scheme %q (only http and https)", u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("Curl: URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("Curl: the URL must not carry credentials (use -H with your own header, if you must)")
	}
	if len(hosts) > 0 && !hostAllowed(u.Hostname(), hosts) {
		return fmt.Errorf("Curl: host %q is not allowed (allowed: %s)",
			u.Hostname(), strings.Join(hosts, ", "))
	}
	return nil
}

// hostAllowed reports whether host matches the allowlist. Entries match
// case-insensitively; a leading "." (or "*.") makes it a suffix match, so
// ".example.com" covers example.com and any subdomain.
func hostAllowed(host string, allowed []string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, entry := range allowed {
		entry = strings.ToLower(strings.TrimSpace(entry))
		entry = strings.TrimPrefix(entry, "*")
		if entry == "" {
			continue
		}
		if entry == host {
			return true
		}
		if strings.HasPrefix(entry, ".") && (host == entry[1:] || strings.HasSuffix(host, entry)) {
			return true
		}
	}
	return false
}

// stringList decodes a JSON array of strings. A missing value is nil, and
// anything else is an error, so the arguments are never re-parsed as a command
// line.
func stringList(v any) ([]string, error) {
	switch list := v.(type) {
	case nil:
		return nil, nil
	case []string:
		return list, nil
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("Curl: 'args' must be a list of strings")
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("Curl: 'args' must be a list of strings")
}

// curlArgv builds the argv for the fetch: the fixed defaults, the model's
// options, then the URL as --url, so a value is not taken for a flag.
func curlArgv(rawURL string, extra []string) ([]string, error) {
	opts, err := curlOptions(extra)
	if err != nil {
		return nil, err
	}
	argv := []string{
		// -q ignores ~/.curlrc.
		"-q",
		// -sS: quiet about progress, loud about errors.
		"-sS",
		// Both the request and every redirect are pinned to http(s): a flag
		// allowlist cannot express that.
		"--proto", "=http,https",
		"--proto-redir", "=http,https",
		"--max-redirs", strconv.Itoa(curlMaxRedirs),
		"--max-time", strconv.Itoa(curlMaxTime),
	}
	argv = append(argv, opts...)
	return append(argv, "--url", rawURL), nil
}

// curlOptions validates the model's options against the allowlist and
// normalizes them into a flag/value sequence: `--header=X` and `-H X` both
// become `--header X`.
func curlOptions(extra []string) ([]string, error) {
	out := make([]string, 0, len(extra))
	for i := 0; i < len(extra); i++ {
		arg := extra[i]
		name, value, hasValue := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if eq := strings.Index(arg, "="); eq >= 0 {
				name, value, hasValue = arg[:eq], arg[eq+1:], true
			}
		} else if !strings.HasPrefix(arg, "-") || arg == "-" {
			return nil, fmt.Errorf("Curl: unexpected argument %q: 'args' holds curl options only (the URL goes in 'url')", arg)
		}
		takesValue, ok := curlFlags[name]
		if !ok {
			return nil, fmt.Errorf("Curl: option %q is not allowed (allowed: %s)", name, curlFlagNames())
		}
		if hasValue && !takesValue {
			return nil, fmt.Errorf("Curl: option %q does not take a value", name)
		}
		if takesValue && !hasValue {
			if i+1 >= len(extra) {
				return nil, fmt.Errorf("Curl: option %q requires a value", name)
			}
			i++
			value = extra[i]
		}
		if takesValue {
			if err := validateCurlValue(name, value); err != nil {
				return nil, err
			}
		}
		out = append(out, name)
		if takesValue {
			out = append(out, value)
		}
	}
	return out, nil
}

// curlFlagNames returns the allowlisted option names, sorted, for error messages.
func curlFlagNames() string {
	names := make([]string, 0, len(curlFlags))
	for name := range curlFlags {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, " ")
}

// validateCurlValue rejects the values that turn an allowed option into a
// different one: header/CRLF injection, @file indirection, a cookie file and an
// unbounded timeout.
func validateCurlValue(name, value string) error {
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("Curl: the value for %q must not contain NUL, CR or LF", name)
	}
	switch name {
	case "-b", "--cookie":
		// curl reads a cookie FILE when the value has no "=": inline only.
		if !strings.Contains(value, "=") || strings.HasPrefix(value, "@") {
			return fmt.Errorf("Curl: %s accepts an inline cookie only (name=value); reading a cookie file is not allowed", name)
		}
	case "-H", "--header", "-A", "--user-agent":
		if strings.HasPrefix(value, "@") {
			return fmt.Errorf("Curl: %s does not accept a file (@path)", name)
		}
	}
	switch name {
	case "--max-time", "--connect-timeout":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("Curl: %s needs a positive integer of seconds, got %q", name, value)
		}
		if n > curlMaxTimeCap {
			return fmt.Errorf("Curl: %s is capped at %d seconds", name, curlMaxTimeCap)
		}
	case "--max-redirs":
		n, err := strconv.Atoi(value)
		if err != nil || n < 1 {
			return fmt.Errorf("Curl: --max-redirs needs a positive integer, got %q", value)
		}
		if n > curlMaxRedirsCap {
			return fmt.Errorf("Curl: --max-redirs is capped at %d", curlMaxRedirsCap)
		}
	case "--max-filesize":
		if n, err := strconv.Atoi(value); err != nil || n < 0 {
			return fmt.Errorf("Curl: --max-filesize needs a non-negative integer of bytes, got %q", value)
		}
	}
	return nil
}

// truncateCurlOutput bounds the body, cutting on a rune boundary so the result
// stays valid UTF-8.
func truncateCurlOutput(out string) string {
	if len(out) <= curlMaxOutput {
		return out
	}
	cut := curlMaxOutput
	for cut > 0 && !utf8.RuneStart(out[cut]) {
		cut--
	}
	return out[:cut] + fmt.Sprintf("\n[output truncated: %d bytes total, showing the first %d]",
		len(out), cut)
}

// glob implements the `glob` tool, like Emacs' `file-expand-wildcards`: PATTERN
// is expanded under PATH (default the cwd), a leading `~` becomes the home
// directory, and relative patterns yield relative matches.
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
// occurrence of SEARCH in PATH with REPLACE, literal and not a regex.
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
