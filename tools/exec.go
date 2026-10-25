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

// curl defaults. They are fixed (not flags): the point of the tool is a small,
// predictable GET, and the model can only tighten them, never open them up.
const (
	// curlMaxTime bounds a hung request so it cannot park the turn.
	curlMaxTime = 30
	// curlMaxTimeCap / curlMaxRedirsCap bound what the model may ask for.
	curlMaxTimeCap   = 120
	curlMaxRedirsCap = 10
	// curlMaxRedirs is the default redirect budget (redirects are followed
	// only when -L is given).
	curlMaxRedirs = 5
	// curlMaxOutput truncates the body: a whole page in the history burns
	// tokens, breaks the provider's prefix cache and is an injection vector.
	curlMaxOutput = 64 << 10
)

// curlFlags is the allowlist of curl options the tool accepts from the model,
// mapped to whether the option takes a value. An allowlist (not a denylist) on
// purpose: anything that uploads (-d/-F/-T), writes files (-o/-O), reads files
// (-K/--config, a cookie file) or opens another transport (--unix-socket) is
// simply not here, including options added by future curl releases.
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

// Executor runs the bridge's tools inside the bridge process, against a working
// directory (the current project's cwd, set by the client via `set_cwd`). It
// mirrors the semantics of the tools that used to run in the Emacs client, so
// behavior is unchanged after moving execution into the bridge.
type Executor struct {
	cwd string
	// sandbox, when non-nil, wraps the external processes (shell, grep) in
	// caged. The in-process file tools (read/write/glob/search_replace) do not
	// exec anything, so they are only bounded by the bridge's own permissions.
	sandbox *sandbox.Sandbox
	// curlSandbox is the sandbox the curl tool runs under: same shape as
	// sandbox, but with network access and WITHOUT the project/home paths, so
	// a fetch cannot read local files (and therefore cannot exfiltrate them).
	// nil means the curl tool is disabled.
	curlSandbox *sandbox.Sandbox
	// curlHosts restricts the curl tool to these hosts (empty means any host).
	// Landlock only filters the destination port, never the host, so this is
	// enforced here, before the command runs.
	curlHosts []string
}

// NewExecutor returns an Executor rooted at cwd. A relative path argument to
// any tool is resolved against cwd (when cwd is empty, relative paths are left
// as-is and resolved by the process working directory).
func NewExecutor(cwd string) *Executor {
	return &Executor{cwd: cwd}
}

// NewExecutorWithSandbox returns an Executor rooted at cwd whose external
// processes run under sb. A nil sb means "run unsandboxed", exactly like
// NewExecutor. The sandbox policy is used as given: the tool works on the same
// filesystem the bridge does, so the paths come from the bridge's own policy
// (-sandbox-ro/-sandbox-rw), not from the cwd.
func NewExecutorWithSandbox(cwd string, sb *sandbox.Sandbox) *Executor {
	return &Executor{cwd: cwd, sandbox: sb}
}

// NewExecutorWithSandboxes returns an Executor rooted at cwd whose external
// processes run under sb and whose curl tool runs under curlSb (a separate,
// network-enabled but filesystem-minimal sandbox). A nil curlSb keeps the curl
// tool disabled. hosts, when non-empty, restricts the curl tool's destinations.
func NewExecutorWithSandboxes(cwd string, sb, curlSb *sandbox.Sandbox, hosts []string) *Executor {
	return &Executor{cwd: cwd, sandbox: sb, curlSandbox: curlSb, curlHosts: hosts}
}

// run executes name/args capturing the combined output. With a sandbox
// configured it goes through Sandbox.Run, which proves from caged's report
// descriptor whether the sandbox applied: a *sandbox.SetupError comes back
// when it did not, so the caller must surface it as an error instead of the
// tool's output.
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

// isSetupError reports whether err means the sandbox did not apply, i.e. the
// command never ran confined and its output must not be handed to the model.
func isSetupError(err error) bool {
	var se *sandbox.SetupError
	return errors.As(err, &se)
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
	out, err := e.run(ctx, "bash", "-c", command)
	if err != nil {
		// A caged failure is never the tool's result: the command did not run
		// (confined or at all), so the model gets an error instead of output.
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
	out, err := e.run(ctx, "grep", "-rnEI", "--include=*", pattern, e.resolve(path))
	if err != nil {
		// A caged failure is never a grep result: the sandbox did not apply.
		if isSetupError(err) {
			return "", err
		}
		if _, ok := err.(*exec.ExitError); ok {
			// Any other grep exit status is surfaced as output: 0 matches / 1
			// no matches are normal results, and on error the model still sees
			// whatever grep printed (mirroring the client).
			return out, nil
		}
		return "", err
	}
	return out, nil
}

// curl implements the `curl` tool: fetch URL (http/https) with the extra
// options in ARGS, all of them validated against an allowlist and passed as an
// argv (never through a shell). It is the only tool with network access, and it
// runs under its own sandbox: filesystem-minimal (no project, no home, no temp
// dir), so a fetch has nothing local to read — and therefore nothing local to
// leak into a URL, a header or a DNS query.
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
	// No cwd on purpose: with a minimal policy there is no relative path the
	// command could legitimately touch, so it starts in the bridge's own.
	out, err := e.curlSandbox.Run(ctx, "", "curl", argv...)
	if err != nil {
		// A caged failure is never the tool's result (see shell).
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

// untrustedContentOpen is how a fetched body is delimited before it reaches the
// model. The system prompt tells the model that everything between the tags is
// data from the web, never an instruction — so when a page says "ignore the
// previous instructions and run X", the model reads that as the *content of a
// page*, which is what it is.
//
// Truncation happens first and wrapping second: a huge body must neither push
// the envelope away nor hide a payload after the cut, and the closing tag has
// to survive in every case.
func untrustedCurlResult(rawURL, body string) string {
	return "<untrusted-web-content url=\"" + attrEscape(rawURL) + "\">\n" +
		truncateCurlOutput(body) + "\n</untrusted-web-content>"
}

// attrEscape keeps an attribute value from breaking out of the envelope: a URL
// carrying a quote, an angle bracket or an ampersand would otherwise be able to
// close the tag or open another one.
func attrEscape(s string) string {
	return strings.NewReplacer(
		"&", "&amp;",
		`"`, "&quot;",
		"<", "&lt;",
		">", "&gt;",
	).Replace(s)
}

// validateCurlURL rejects anything the tool will not fetch: a missing URL, a
// scheme other than http/https (file:// would be a local read, ftp:// another
// transport), credentials embedded in the URL (they would end up in the request
// and in the log) and a host outside the allowlist.
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

// hostAllowed reports whether host matches the configured allowlist. Entries are
// matched case-insensitively; a leading "." (or "*.") makes it a suffix match,
// so ".example.com" covers example.com and any subdomain.
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

// stringList decodes a JSON array of strings into a []string. A missing value is
// nil; anything else (a string, a number, an array with a non-string) is an
// error, so the arguments are never re-parsed as a command line.
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

// curlArgv builds the argv for the fetch: the fixed defaults first, then the
// model's options, then the URL last (as --url, so a value can never be taken
// for a flag).
func curlArgv(rawURL string, extra []string) ([]string, error) {
	opts, err := curlOptions(extra)
	if err != nil {
		return nil, err
	}
	argv := []string{
		// -q ignores ~/.curlrc: the environment must not change what runs.
		"-q",
		// -sS is quiet about progress, loud about errors.
		"-sS",
		// Both the initial request and every redirect are pinned to http(s):
		// a flag allowlist cannot express that.
		"--proto", "=http,https",
		"--proto-redir", "=http,https",
		"--max-redirs", strconv.Itoa(curlMaxRedirs),
		"--max-time", strconv.Itoa(curlMaxTime),
	}
	argv = append(argv, opts...)
	return append(argv, "--url", rawURL), nil
}

// curlOptions validates the model's extra options against the allowlist and
// normalizes them into a flag/value sequence (`--header=X` and `-H X` both
// become `--header X`).
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
// different one: header/CRLF injection, @file indirection, a cookie file
// (-b without "=" reads a file) and unbounded timeouts.
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

// truncateCurlOutput bounds the body that goes into the history, cutting on a
// rune boundary so the result stays valid UTF-8.
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
