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
	"strings"
	"testing"
	"unicode/utf8"

	"llm-bridge/sandbox"
)

// TestValidateCurlURL covers the URL gate: only an absolute http/https URL with
// a host and no embedded credentials may reach curl, and the optional host
// allowlist is applied before anything runs.
func TestValidateCurlURL(t *testing.T) {
	valid := []string{
		"https://poraodojuca.dev",
		"http://localhost:8000/x?y=1",
		"https://example.com/a/b#frag",
	}
	for _, raw := range valid {
		if err := validateCurlURL(raw, nil); err != nil {
			t.Errorf("validateCurlURL(%q) = %v, want nil", raw, err)
		}
	}

	invalid := map[string]string{
		"":                         "missing",
		"file:///etc/passwd":       "scheme",
		"ftp://example.com/x":      "scheme",
		"dict://example.com:2628":  "scheme",
		"https://":                 "no host",
		"https://user:pass@x.test": "credentials",
		"://nope":                  "invalid URL",
	}
	for raw, want := range invalid {
		err := validateCurlURL(raw, nil)
		if err == nil {
			t.Errorf("validateCurlURL(%q) = nil, want an error mentioning %q", raw, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validateCurlURL(%q) = %v, want it to mention %q", raw, err, want)
		}
	}

	// Host allowlist: exact, subdomain (leading dot) and "any subdomain" forms.
	allowed := []string{"example.com", ".poraodojuca.dev"}
	for _, raw := range []string{
		"https://example.com/",
		"https://poraodojuca.dev/x",
		"https://docs.poraodojuca.dev/x",
		"https://EXAMPLE.com/", // case-insensitive
	} {
		if err := validateCurlURL(raw, allowed); err != nil {
			t.Errorf("validateCurlURL(%q, allowlist) = %v, want nil", raw, err)
		}
	}
	err := validateCurlURL("https://atacante.test/x", allowed)
	if err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Fatalf("host outside the allowlist: err = %v, want 'not allowed'", err)
	}
	if !strings.Contains(err.Error(), "example.com") {
		t.Errorf("error should list the allowed hosts: %v", err)
	}
}

// TestHostAllowed covers the matcher directly, including the empty and
// wildcard-prefixed entries.
func TestHostAllowed(t *testing.T) {
	if hostAllowed("example.com", nil) {
		t.Error("an empty allowlist must not allow anything (the caller checks len)")
	}
	if !hostAllowed("www.example.com", []string{"*.example.com"}) {
		t.Error("*.example.com should match www.example.com")
	}
	if hostAllowed("notexample.com", []string{".example.com"}) {
		t.Error(".example.com must not match notexample.com")
	}
	if !hostAllowed("example.com.", []string{"example.com"}) {
		t.Error("a trailing dot in the host should be ignored")
	}
	if hostAllowed("example.com", []string{"", "  "}) {
		t.Error("blank entries must be ignored, not treated as a wildcard")
	}
}

// TestCurlOptions covers the allowlist: allowed flags (short, long and
// --flag=value forms) are normalized, and everything else is rejected —
// including the options that would upload, write a file, read a file or open
// another transport.
func TestCurlOptions(t *testing.T) {
	got, err := curlOptions([]string{"-H", "X-Test: 1", "--location", "--header=Accept: */*", "-I"})
	if err != nil {
		t.Fatalf("curlOptions: %v", err)
	}
	want := "-H|X-Test: 1|--location|--header|Accept: */*|-I"
	if strings.Join(got, "|") != want {
		t.Fatalf("curlOptions = %q, want %q", strings.Join(got, "|"), want)
	}

	rejected := map[string]string{
		"-d":                  "not allowed",
		"--data=@/etc/passwd": "not allowed",
		"-T":                  "not allowed",
		"-o":                  "not allowed",
		"-O":                  "not allowed",
		"-K":                  "not allowed",
		"--config=/tmp/x":     "not allowed",
		"--unix-socket":       "not allowed",
		"-X":                  "not allowed",
		"https://evil.test":   "unexpected argument",
		"-":                   "unexpected argument",
		"--location=yes":      "does not take a value",
		"-H":                  "requires a value",
	}
	for arg, want := range rejected {
		_, err := curlOptions([]string{arg})
		if err == nil {
			t.Errorf("curlOptions(%q) = nil, want an error mentioning %q", arg, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("curlOptions(%q) = %v, want it to mention %q", arg, err, want)
		}
	}
}

// TestValidateCurlValue covers the values that would turn an allowed option into
// a different one: CRLF/NUL injection, @file indirection, a cookie file, and
// timeouts/redirect counts outside the caps.
func TestValidateCurlValue(t *testing.T) {
	bad := []struct{ name, value, want string }{
		{"-H", "X: a\r\nEvil: b", "must not contain"},
		{"-H", "\x00", "must not contain"},
		{"-H", "@/etc/passwd", "does not accept a file"},
		{"-A", "@file", "does not accept a file"},
		{"-b", "/home/u/.cookies", "inline cookie"},
		{"-b", "@cookies.txt", "inline cookie"},
		{"--max-time", "0", "positive integer"},
		{"--max-time", "999", "capped"},
		{"--connect-timeout", "-1", "positive integer"},
		{"--max-redirs", "99", "capped"},
		{"--max-redirs", "abc", "positive integer"},
		{"--max-filesize", "-5", "non-negative"},
	}
	for _, c := range bad {
		err := validateCurlValue(c.name, c.value)
		if err == nil {
			t.Errorf("validateCurlValue(%q, %q) = nil, want an error", c.name, c.value)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("validateCurlValue(%q, %q) = %v, want it to mention %q", c.name, c.value, err, c.want)
		}
	}

	good := []struct{ name, value string }{
		{"-H", "X-Test: 1"},
		{"-b", "session=abc"},
		{"--max-time", "10"},
		{"--max-redirs", "1"},
		{"--max-filesize", "0"},
	}
	for _, c := range good {
		if err := validateCurlValue(c.name, c.value); err != nil {
			t.Errorf("validateCurlValue(%q, %q) = %v, want nil", c.name, c.value, err)
		}
	}
}

// TestCurlArgvDefaults covers the fixed part of the argv: the defaults come
// first, the model's options after them and the URL last (as --url, so it can
// never be read as a flag).
func TestCurlArgvDefaults(t *testing.T) {
	argv, err := curlArgv("https://example.com/x", []string{"-H", "A: b"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(argv, " ")
	for _, want := range []string{
		"-q",
		"-sS",
		"--proto =http,https",
		"--proto-redir =http,https",
		"--max-redirs 5",
		"--max-time 30",
		"-H A: b",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv = %q, missing %q", joined, want)
		}
	}
	if last := argv[len(argv)-2:]; last[0] != "--url" || last[1] != "https://example.com/x" {
		t.Errorf("argv should end with --url <url>, got %v", last)
	}
	// -q must be first: curl only honors it as the first argument.
	if argv[0] != "-q" {
		t.Errorf("argv[0] = %q, want -q", argv[0])
	}
}

// TestTruncateCurlOutput covers the output bound: short bodies pass untouched,
// long ones are cut with a note and stay valid UTF-8.
func TestTruncateCurlOutput(t *testing.T) {
	short := "body"
	if got := truncateCurlOutput(short); got != short {
		t.Errorf("short output changed: %q", got)
	}

	// A body made of 2-byte runes guarantees the cut falls inside a rune.
	long := strings.Repeat("é", curlMaxOutput)
	got := truncateCurlOutput(long)
	if !strings.Contains(got, "[output truncated") {
		t.Fatalf("long output not marked as truncated: %q", got[:40])
	}
	if !utf8.ValidString(got) {
		t.Error("truncated output is not valid UTF-8")
	}
	if !strings.HasPrefix(got, strings.Repeat("é", 10)) {
		t.Error("truncated output should keep the beginning of the body")
	}
}

// TestTruncateCurlOutputCutsOnRuneBoundary covers the other side of the UTF-8
// cut: when the limit lands *inside* a multi-byte rune, the cut has to walk back
// to the rune start. Without that walk the model would receive a broken rune.
func TestTruncateCurlOutputCutsOnRuneBoundary(t *testing.T) {
	// One ASCII byte short of the limit, then 2-byte runes: byte at the limit
	// is the continuation byte of the first "é".
	long := strings.Repeat("a", curlMaxOutput-1) + strings.Repeat("é", 10)
	got := truncateCurlOutput(long)
	if !utf8.ValidString(got) {
		t.Fatalf("truncated output is not valid UTF-8: %q", got[len(got)-20:])
	}
	if !strings.HasPrefix(got, strings.Repeat("a", curlMaxOutput-1)) {
		t.Fatal("truncated output lost the body prefix")
	}
	if !strings.Contains(got, "[output truncated") {
		t.Fatal("truncated output is not marked as truncated")
	}
	// The cut walked back to the rune start, so nothing of the first "é" leaked.
	if strings.Contains(strings.SplitN(got, "\n[output truncated", 2)[0], "é") {
		t.Error("a partial rune survived the cut")
	}
}

// TestStringList covers both accepted shapes: a decoded JSON array ([]any) and a
// []string handed in directly, which is what an in-process caller passes.
func TestStringList(t *testing.T) {
	direct, err := stringList([]string{"-L", "--header=X: 1"})
	if err != nil {
		t.Fatalf("stringList([]string): %v", err)
	}
	if strings.Join(direct, "|") != "-L|--header=X: 1" {
		t.Errorf("stringList([]string) = %v", direct)
	}

	if got, err := stringList(nil); err != nil || got != nil {
		t.Errorf("stringList(nil) = (%v, %v), want (nil, nil)", got, err)
	}

	decoded, err := stringList([]any{"-L", "--compressed"})
	if err != nil {
		t.Fatalf("stringList([]any): %v", err)
	}
	if strings.Join(decoded, "|") != "-L|--compressed" {
		t.Errorf("stringList([]any) = %v", decoded)
	}

	for _, bad := range []any{"-L", 3, []any{"-L", 1}} {
		if _, err := stringList(bad); err == nil {
			t.Errorf("stringList(%v) should error", bad)
		}
	}
}

// TestCurlDisabledWithoutSandbox covers the case where the tool is not
// configured: the call fails instead of running curl unsandboxed.
func TestCurlDisabledWithoutSandbox(t *testing.T) {
	e := NewExecutorWithSandboxes(t.TempDir(), nil, nil, nil)
	_, err := e.Execute(context.Background(), "curl", `{"url":"https://example.com"}`)
	if err == nil || !strings.Contains(err.Error(), "network access is disabled") {
		t.Fatalf("err = %v, want 'network access is disabled'", err)
	}
}

// TestCurlRunsThroughItsOwnSandbox covers the happy path with a stub caged that
// prints the argv: the tool runs curl under curlSandbox (not the tools one) with
// the defaults, the model's options and the URL.
func TestCurlRunsThroughItsOwnSandbox(t *testing.T) {
	curlSb, err := sandbox.New(fakeCaged(t, `printf '%s' "$*"`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	// The tools sandbox is a different, deliberately broken one: using it here
	// would surface as an error, which is what makes this test meaningful.
	toolsSb, err := sandbox.New(fakeCaged(t, `printf 'error: wrong sandbox' >&3; exit 125`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandboxes(t.TempDir(), toolsSb, curlSb, nil)

	out, err := e.Execute(context.Background(), "curl",
		`{"url":"https://poraodojuca.dev/x","args":["-H","X-Test: 1","-L"]}`)
	if err != nil {
		t.Fatalf("curl: %v", err)
	}
	for _, want := range []string{"-q", "--url https://poraodojuca.dev/x", "-H X-Test: 1", "-L"} {
		if !strings.Contains(out, want) {
			t.Errorf("argv = %q, missing %q", out, want)
		}
	}
}

// TestCurlSetupFailureIsAnError covers the sandbox failure path: a caged that
// could not build the sandbox is an error, never the tool's output.
func TestCurlSetupFailureIsAnError(t *testing.T) {
	sb, err := sandbox.New(fakeCaged(t, "printf 'error allowing read access\\n' >&3; exit 125"), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandboxes(t.TempDir(), nil, sb, nil)
	out, err := e.Execute(context.Background(), "curl", `{"url":"https://example.com"}`)
	if err == nil {
		t.Fatalf("expected an error, got output %q", out)
	}
	if !strings.Contains(err.Error(), "sandbox:") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestCurlExitStatusIsAnOutput covers a fetch that ran and failed on its own
// (curl's exit status, e.g. 6 for "could not resolve host"): the status is
// reported as part of the result, like the shell tool.
func TestCurlExitStatusIsAnOutput(t *testing.T) {
	sb, err := sandbox.New(fakeCaged(t, `exit 6`), sandbox.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	e := NewExecutorWithSandboxes(t.TempDir(), nil, sb, nil)
	out, err := e.Execute(context.Background(), "curl", `{"url":"https://example.com"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "[curl exited with status 6]") {
		t.Fatalf("output = %q, want the curl status line", out)
	}
}

// TestCurlRejectsBadArgs covers the argument shapes the tool refuses before
// running anything: a non-list "args" and a position URL passed as an option.
func TestCurlRejectsBadArgs(t *testing.T) {
	e := NewExecutorWithSandboxes(t.TempDir(), nil, &sandbox.Sandbox{Bin: "caged"}, nil)
	for _, args := range []string{
		`{"url":"https://example.com","args":"-L"}`,
		`{"url":"https://example.com","args":[1,2]}`,
		`{"url":"https://example.com","args":["https://other.test"]}`,
	} {
		if _, err := e.Execute(context.Background(), "curl", args); err == nil {
			t.Errorf("Execute(%s) should error", args)
		}
	}
}
