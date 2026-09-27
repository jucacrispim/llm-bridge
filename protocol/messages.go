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
	// System optionally overrides the system prompt for this and subsequent
	// prompts. Like Model/Provider, the override persists between prompts.
	System string `json:"system,omitempty"`
	// SystemPrompt is an alias for System.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Images are optional image attachments for this prompt. Each image may be
	// given by path (a local file, resolved against the current cwd), by url
	// (an external http(s) link passed through to the provider) or inline as
	// base64 data / data URL (used for clipboard-pasted images).
	Images []ImageParam `json:"images,omitempty"`
}

// ImageParam describes one image attached to a prompt. The bridge resolves it
// into an inline image (path/data) or a pass-through URL and then maps it to
// the active provider's wire format.
type ImageParam struct {
	// Path is a local file path: absolute, or relative to the current cwd.
	Path string `json:"path,omitempty"`
	// URL is an external http(s) image URL, passed through to the provider.
	URL string `json:"url,omitempty"`
	// Data is inline base64 image data: raw base64, or a full data: URL
	// (data:<mime>;base64,<payload>). Used for clipboard-pasted images.
	Data string `json:"data,omitempty"`
	// MIME optionally declares the media type (image/png, image/jpeg,
	// image/gif, image/webp); otherwise it is detected from the bytes.
	MIME string `json:"mime_type,omitempty"`
	// Detail optionally controls image processing ("low"/"high"/"original"/
	// "auto" for DeepSeek). Ignored by providers without the concept.
	Detail string `json:"detail,omitempty"`
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
