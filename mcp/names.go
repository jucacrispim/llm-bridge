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

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// maxToolName is what the providers accept. A longer name is truncated and
// tagged, because two tools truncated at the same point would collide.
const maxToolName = 64

// validName matches the server names that may become part of a tool name.
var validName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// exposedName is the name the model sees for a tool: the server and the tool,
// so two servers may both serve "read".
func exposedName(server, tool string) string {
	full := "mcp__" + sanitizeName(server) + "__" + sanitizeName(tool)
	if len(full) <= maxToolName {
		return full
	}
	sum := sha256.Sum256([]byte(full))
	suffix := "__" + hex.EncodeToString(sum[:4])
	return full[:maxToolName-len(suffix)] + suffix
}

// sanitizeName keeps the characters the providers accept and replaces the
// rest. A dot is replaced too: OpenAI rejects it in some tool names.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
