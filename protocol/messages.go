package protocol

import "encoding/json"

type Method string

const (
	MethodPrompt            Method = "prompt"
	MethodCancel            Method = "cancel"
	MethodSetCwd            Method = "set_cwd"
	MethodSetKnowledgeBases Method = "set_knowledge_bases"
	MethodToolResult        Method = "tool_result"
	MethodQuit              Method = "quit"
)

type Event string

const (
	EventReady        Event = "ready"
	EventChunk        Event = "chunk"
	EventThinking     Event = "thinking"
	EventToolCall     Event = "tool_call"
	EventTurnEnd      Event = "turn_end"
	EventError        Event = "error"
	EventCancelled    Event = "cancelled"
	EventUsageDelta   Event = "usage_delta"
	EventFilesChanged Event = "files_changed"
	EventHookAction   Event = "hook_action"
)

type Command struct {
	Method Method          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type PromptParams struct {
	Text  string `json:"text"`
	Model string `json:"model"`
	// Provider optionally switches the LLM provider for this and subsequent
	// prompts (e.g. "deepseek" or "google"). Omitted keeps the current
	// provider. Like Model/Thinking/ReasoningEffort, the override persists
	// between prompts.
	Provider string `json:"provider,omitempty"`
	// Thinking optionally overrides thinking mode for the conversation:
	// true → deepseek-reasoner, false → deepseek-chat. Omitted keeps the
	// provider's configured mode.
	Thinking *bool `json:"thinking,omitempty"`
	// ReasoningEffort optionally overrides reasoning_effort when thinking is
	// on (e.g. "low", "medium", "high"). An empty string disables sending the
	// parameter. Omitted keeps the provider's configured value.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
}

type ToolResultParams struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
	Status string          `json:"status"`
}

type ToolCallResult struct {
	ID    string                 `json:"id"`
	Name  string                 `json:"name"`
	Input map[string]interface{} `json:"input"`
}

type ChunkEvent struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

type TurnEndEvent struct {
	Event      Event    `json:"event"`
	StopReason string   `json:"stop_reason"`
	ContextPct *float64 `json:"context_pct"`
	Model      string   `json:"model"`
}
