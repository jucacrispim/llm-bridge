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

// Classification helpers for the confirmation step. They answer three
// questions the server asks before offering a tool to the human:
//
//   - does this call mutate anything? (a `knowledge add` does, a `knowledge
//     show` does not, so the same tool cannot be classified once for all)
//   - what does this one-liner actually run? (models join everything with &&
//     and |, and a human who approves "build the project" approves all of it)
//   - how bad is it? (a coarse risk label, deliberately heuristic)
//
// Everything here is advisory and cheap: it never blocks a call by itself. The
// one hard limit, MaxShellSegments, is enforced by the server, which refuses to
// offer an approval that would run dozens of commands at once.

import (
	"encoding/json"
	"strings"
)

// MaxShellSegments is the most commands a single `shell` call may contain. A
// one-liner is approved with one keystroke, so it is the natural place to hide
// extra work: past this limit the server refuses to offer it and tells the
// model to split the command into separate calls.
const MaxShellSegments = 10

// TaintWindow is how many consecutive bytes an argument must share with
// content fetched earlier in the session before it is flagged as copied. Long
// enough not to match boilerplate, short enough to survive a reflowed copy.
const TaintWindow = 16

// MutatesCall reports whether a specific tool call mutates state, which is what
// decides between running it inline and offering it for confirmation.
//
// For every tool but `knowledge` this is the static classification. `knowledge`
// is per call: add/delete/reset write to a persistent store, while show/search
// only read it. Treating the whole tool as read-only would let an injected page
// plant a note with no human in the loop; treating it as always mutating would
// turn every lookup into a click. An unknown or malformed command is treated as
// mutating, so a call we cannot classify is confirmed rather than executed.
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
// `||`, `|`, a lone `&` and newlines, without splitting inside quotes. It is
// what lets the client show "this approval runs 4 commands" instead of one long
// line, and what the segment limit counts.
//
// Redirections are not separators: `cmd > out 2>&1` is one command, and the `&`
// of `2>&1`, `>&2`, `<&3` and of the `&>`/`&>>` form belongs to the redirect.
// Splitting there used to report `git describe --tags 2>&1 || true` as four
// commands (`git describe --tags 2>`, `1`, ...) and to inflate the count the
// limit is applied to. Heredoc bodies are still not parsed, so a `&` inside one
// is treated as a separator.
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
// there. The list is deliberately small and readable: it is a hint for the
// human (and for a client that may want a typed confirmation), never a verdict,
// and a false positive only costs attention.
var riskPatterns = []struct {
	level   string
	markers []string
}{
	{"high", []string{
		"rm -rf", "rm -fr", "rm -r", "mkfs", "dd of=", "shred ",
		"chmod -r", "chown -r", ":(){", "> /dev/sd", "git push",
		"sudo ", "su -", "doas ", "shutdown", "reboot", "kill -9",
		"curl ", "wget ", "nc ", "ncat ", "ssh ", "scp ", "rsync ",
		"eval ", "base64 -d", "sh -c", "bash -c",
	}},
	{"medium", []string{
		"git commit", "git checkout", "git reset", "npm publish",
		"pip install", "go install", "make install", "docker ",
	}},
}

// pipeToShell matches `... | sh` / `| bash` / `| python`, the classic way to
// turn downloaded text into code.
var pipeToShell = []string{"| sh", "| bash", "| zsh", "| dash", "| python", "| perl", "| ruby", "| node"}

// Risk classifies a shell command as "high", "medium" or "low". command is the
// original string (needed for the pipe-to-shell shape, which splitting on `|`
// destroys) and segments the result of Segments.
func Risk(command string, segments []string) string {
	lower := strings.ToLower(command)
	for _, marker := range pipeToShell {
		if strings.Contains(lower, marker) {
			return "high"
		}
	}
	level := "low"
	for _, seg := range segments {
		seg = strings.ToLower(seg)
		for _, rule := range riskPatterns {
			for _, marker := range rule.markers {
				if strings.Contains(seg, marker) {
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

// Tainted reports whether any argument reproduces a run of TaintWindow bytes of
// content fetched earlier in the session — the shape of "read this page, then
// run what it says". It is advisory: a legitimate argument that happens to
// quote the page also matches, which is exactly the case worth showing.
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
// a flat list on purpose: whichever field the model filled, the value is what
// matters.
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
