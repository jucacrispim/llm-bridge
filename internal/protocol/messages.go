package protocol

import "encoding/json"

type Method string

const (
	MethodPrompt            Method = "prompt"
	MethodCancel            Method = "cancel"
	MethodStatus            Method = "status"
	MethodSetCwd            Method = "set_cwd"
	MethodSetKnowledgeBases Method = "set_knowledge_bases"
	MethodToolResult        Method = "tool_result"
	MethodQuit              Method = "quit"
)

type Event string

const (
	EventReady        Event = "ready"
	EventChunk        Event = "chunk"
	EventToolCall     Event = "tool_call"
	EventTurnEnd      Event = "turn_end"
	EventError        Event = "error"
	EventCancelled    Event = "cancelled"
	EventStatus       Event = "status"
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
}

type ToolResultParams struct {
	ID     string `json:"id"`
	Output string `json:"output"`
	Status string `json:"status"`
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
	Event      Event       `json:"event"`
	StopReason string      `json:"stop_reason"`
	ContextPct *float64    `json:"context_pct"`
	Metering   interface{} `json:"metering"`
	Model      string      `json:"model"`
}
