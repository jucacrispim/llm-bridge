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
			Description: "Read a file from disk",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`),
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
			Description: "Search for a pattern in files",
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
