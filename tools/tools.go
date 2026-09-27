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
	"encoding/json"

	"llm-bridge/llm"
)

// All returns the tools that the bridge exposes to the LLM.
func All() []llm.Tool {
	return []llm.Tool{
		{
			Name:        "read",
			Description: "Read a file from disk. Optional 'offset' (0-based line index to start at) and 'limit' (max number of lines to return) select a slice of the file by lines; without them the whole file is returned.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"offset":{"type":"integer"},"limit":{"type":"integer"}},"required":["path"]}`),
		},
		{
			Name:        "write",
			Description: "Write content to a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`),
		},
		{
			Name:        "shell",
			Description: "Execute a shell command",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string"}},"required":["command"]}`),
		},
		{
			Name:        "grep",
			Description: "Search file contents for a POSIX extended regular expression (like 'grep -E'). The pattern is a regex: to match a literal string, escape regex metacharacters ('.', '[', ']', '(', ')', '{', '}', '+', '?', '|', '*', '^', '$', '\\'). Returns matching lines as 'file:line:text'.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`),
		},
		{
			Name:        "glob",
			Description: "Find files matching a glob pattern",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"pattern":{"type":"string"},"path":{"type":"string"}},"required":["pattern"]}`),
		},
		{
			Name:        "search_replace",
			Description: "Search for an exact string in a file and replace the first occurrence with a new string",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"search":{"type":"string"},"replace":{"type":"string"}},"required":["path","search","replace"]}`),
		},
		{
			Name:        "knowledge",
			Description: "Manage the project's local knowledge base (scoped to the current working directory). Commands: 'show' lists stored items; 'search' with a query finds relevant notes; 'add' with a label and text stores or UPDATES a note (label is the key: re-adding an existing label replaces it); 'delete' with a label removes a note; 'reset' clears the KB and rebuilds it from the curated seed. Use it to store/update/remove notes about what you learn here.",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","enum":["show","search","add","delete","reset"]},"query":{"type":"string"},"label":{"type":"string"},"text":{"type":"string"},"limit":{"type":"integer"}},"required":["command"]}`),
		},
	}
}
