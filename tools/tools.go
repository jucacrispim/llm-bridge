package tools

import (
	"encoding/json"

	"llm-bridge/llm"
)

// All returns the seven tools that the bridge exposes to the LLM.
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
			Name:        "code",
			Description: "Provide a code snippet or execute code",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}`),
		},
		{
			Name:        "search_replace",
			Description: "Search for an exact string in a file and replace the first occurrence with a new string",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"},"search":{"type":"string"},"replace":{"type":"string"}},"required":["path","search","replace"]}`),
		},
	}
}
