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

// Classification helpers for the confirmation step: does this call mutate
// anything, what does this one-liner actually run, and how bad is it. The
// answers are advisory and cheap. The one hard limit, MaxShellSegments, is
// enforced by the server.

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
)

// MaxShellSegments is the most commands one `shell` call may contain. A
// one-liner is approved with one keystroke, so past this limit the server
// refuses it and tells the model to split the command.
const MaxShellSegments = 10

// TaintWindow is how many consecutive bytes an argument must share with content
// fetched earlier in the session before it counts as copied. Short enough to
// survive a reflowed copy, long enough to miss boilerplate.
const TaintWindow = 16

// MutatesCall reports whether a specific call mutates state, which decides
// between running it inline and offering it for confirmation.
//
// Every tool but `knowledge` is classified once. `knowledge` is per call:
// add/delete/reset write to a persistent store, while show/search read it. An
// unknown or malformed command counts as mutating, so it is confirmed.
func MutatesCall(name, rawArgs string) bool {
	if name != "knowledge" {
		return !IsReadOnly(name)
	}
	var a struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(rawArgs), &a); err != nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(a.Command)) {
	case "show", "search":
		return false
	case "add", "delete", "reset":
		return true
	}
	return true
}

// CommandOf extracts the `command` argument of a shell call, or "".
func CommandOf(rawArgs string) string {
	var a struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal([]byte(rawArgs), &a)
	return a.Command
}

// Segments splits a shell one-liner into the commands it runs: on `;`, `&&`,
// `||`, `|`, a lone `&` and newlines, without splitting inside quotes.
//
// A redirect is not a separator: the `&` of `2>&1` belongs to the redirect, so
// `git describe --tags 2>&1 || true` is two commands, not four. A heredoc body
// is not parsed, so a `&` inside one splits.
func Segments(command string) []string {
	var (
		out     []string
		cur     strings.Builder
		quote   rune
		escaped bool
		flush   func()
		runes   = []rune(command)
	)
	flush = func() {
		if seg := strings.TrimSpace(cur.String()); seg != "" {
			out = append(out, seg)
		}
		cur.Reset()
	}
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if escaped {
			cur.WriteRune(r)
			escaped = false
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			cur.WriteRune(r)
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			cur.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
			cur.WriteRune(r)
		case '\n':
			flush()
		case ';':
			flush()
		case '>', '<':
			// A redirect, not a separator. The `&` that may follow it
			// (`2>&1`, `>&2`, `<&3`) is part of the redirect, so write it
			// here instead of letting the `&` case below split the line.
			cur.WriteRune(r)
			if i+1 < len(runes) && runes[i+1] == '&' {
				i++
				cur.WriteRune(runes[i])
			}
		case '&':
			// `&&` is one separator; `&>` and `&>>` are redirects.
			if i+1 < len(runes) {
				switch runes[i+1] {
				case '&':
					i++
					flush()
					continue
				case '>':
					cur.WriteRune(r)
					continue
				}
			}
			flush()
		case '|':
			// `||` is one separator; `|&` pipes stdout and stderr together
			// into the next command, so its `&` is not a separator of its own.
			if i+1 < len(runes) && (runes[i+1] == '|' || runes[i+1] == '&') {
				i++
			}
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// riskPatterns maps a coarse risk level to the markers that put a command
// there. It is a hint for the human, not a verdict.
//
// The markers are word-boundary regexps because most are two or three letters
// long: matching "nc " as a substring flagged `func ` and `sync `, so `go test
// -run TestFunc` came out high risk. `\bnc\b` catches `nc -l 1234` and leaves
// `func`, `sync`, `TestSync` and `encore` alone. Flag clusters are written
// `-[a-z]*r`, so `rm -rf`, `rm -fr`, `rm -r` and `rm -rfv` all match.
var riskPatterns = []struct {
	level   string
	markers []*regexp.Regexp
}{
	{"high", []*regexp.Regexp{
		regexp.MustCompile(`\brm\s+-[a-z]*r`), // -r, -rf, -fr, -rfv
		regexp.MustCompile(`\bmkfs`),
		regexp.MustCompile(`\bdd\b.*\bof=`),
		regexp.MustCompile(`\bshred\b`),
		regexp.MustCompile(`\bch(mod|own)\s+-[a-z]*r`),
		regexp.MustCompile(`:\(\)\{`),
		regexp.MustCompile(`>\s*/dev/sd`),
		regexp.MustCompile(`\bgit\s+push\b`),
		regexp.MustCompile(`\bsudo\b`),
		regexp.MustCompile(`\bsu\s+-`),
		regexp.MustCompile(`\bdoas\b`),
		regexp.MustCompile(`\bshutdown\b`),
		regexp.MustCompile(`\breboot\b`),
		regexp.MustCompile(`\bkill\s+-9\b`),
		regexp.MustCompile(`\bcurl\b`),
		regexp.MustCompile(`\bwget\b`),
		regexp.MustCompile(`\bnc\b`),
		regexp.MustCompile(`\bncat\b`),
		regexp.MustCompile(`\bssh\b`),
		regexp.MustCompile(`\bscp\b`),
		regexp.MustCompile(`\brsync\b`),
		regexp.MustCompile(`\beval\b`),
		regexp.MustCompile(`\bbase64\s+-d\b`),
		regexp.MustCompile(`\bsh\s+-c\b`),
		regexp.MustCompile(`\bbash\s+-c\b`),
		// The directory the next session reads its context from and execs the
		// hooks of, so a command that names it changes every later prompt. The
		// trailing group keeps `.llm-bridge-backup` out.
		regexp.MustCompile(`\.llm-bridge(/|["'\s]|$)`),
	}},
	{"medium", []*regexp.Regexp{
		regexp.MustCompile(`\bgit\s+commit\b`),
		regexp.MustCompile(`\bgit\s+checkout\b`),
		regexp.MustCompile(`\bgit\s+reset\b`),
		regexp.MustCompile(`\bnpm\s+publish\b`),
		regexp.MustCompile(`\bpip[0-9.]*\s+install\b`),
		regexp.MustCompile(`\bgo\s+install\b`),
		regexp.MustCompile(`\bmake\s+install\b`),
		regexp.MustCompile(`\bdocker\b`),
	}},
}

// pipeToShell matches `... | sh` / `| bash` / `| python`, the way downloaded
// text becomes code. The trailing \b keeps `| shellcheck` and `| ssh` out.
var pipeToShell = regexp.MustCompile(`\|\s*(sh|bash|zsh|dash|python[0-9.]*|perl|ruby|node)\b`)

// Risk classifies a shell command as "high", "medium" or "low". command is the
// original string, which the pipe-to-shell shape needs because splitting on `|`
// destroys it; segments is the output of Segments.
func Risk(command string, segments []string) string {
	if pipeToShell.MatchString(strings.ToLower(command)) {
		return "high"
	}
	level := "low"
	for _, seg := range segments {
		seg = strings.ToLower(seg)
		for _, rule := range riskPatterns {
			for _, marker := range rule.markers {
				if marker.MatchString(seg) {
					if rule.level == "high" {
						return "high"
					}
					level = "medium"
				}
			}
		}
	}
	return level
}

// sensitiveDir is the directory the bridge reads the conversation context and
// the hook scripts from. A write into it changes every later session.
const sensitiveDir = ".llm-bridge"

// PathRisk returns "high" when path lands inside a .llm-bridge directory, and
// "" otherwise. cwd resolves a relative path, the same way the tool will.
//
// This is the write counterpart of the shell marker: the path is a structured
// field, so the check is exact rather than a guess over a command line.
func PathRisk(cwd, path string) string {
	path = expandHome(strings.TrimSpace(path))
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) && cwd != "" {
		path = filepath.Join(cwd, path)
	}
	for _, elem := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if elem == sensitiveDir {
			return "high"
		}
	}
	return ""
}

// Tainted reports whether any argument reproduces a run of TaintWindow bytes of
// content fetched earlier in the session, the shape of "read this page, then run
// what it says". It is advisory: a legitimate argument that quotes the page also
// matches.
func Tainted(args []string, fetched string) bool {
	if fetched == "" {
		return false
	}
	source := squash(fetched)
	for _, arg := range args {
		squashed := squash(arg)
		if len(squashed) < TaintWindow {
			continue
		}
		for i := 0; i+TaintWindow <= len(squashed); i++ {
			if strings.Contains(source, squashed[i:i+TaintWindow]) {
				return true
			}
		}
	}
	return false
}

// squash collapses whitespace so a command copied from a document compares
// equal even though the model reflowed the lines.
func squash(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// Args returns every string argument of a tool call, for the taint check. It is
// a flat list: whichever field the model filled, the value is what matters.
func Args(rawArgs string) []string {
	var m map[string]any
	if err := json.Unmarshal([]byte(rawArgs), &m); err != nil {
		return nil
	}
	out := make([]string, 0, len(m))
	for _, v := range m {
		switch value := v.(type) {
		case string:
			out = append(out, value)
		case []any:
			for _, item := range value {
				if s, ok := item.(string); ok {
					out = append(out, s)
				}
			}
		}
	}
	return out
}
