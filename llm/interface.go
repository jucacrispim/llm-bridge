package llm

import (
	"context"
	"encoding/json"
)

const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleSystem    = "system"
)

type Message struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
	// Reasoning is the model's chain-of-thought (deepseek's reasoning_content)
	// that accompanied this message, produced only when thinking mode is on.
	// It is kept in the history and sent back to the API for assistant messages
	// that performed tool calls (DeepSeek's thinking mode returns a 400 error
	// otherwise). For final answers without tool calls it is omitted from
	// requests to save tokens.
	Reasoning string
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type ChatRequest struct {
	Messages []Message
	Tools    []Tool
	Model    string
	// Thinking optionally overrides the provider's thinking mode for this
	// request. nil means "use the provider's configured mode"; true/false
	// switches between deepseek-reasoner and deepseek-chat unless Model is set.
	Thinking *bool
	// ReasoningEffort optionally overrides the provider's reasoning_effort
	// when thinking is on. nil means "use the provider's configured value";
	// non-nil wins over the provider (an empty string disables sending the
	// parameter). Ignored when thinking is off.
	ReasoningEffort *string
	// OnReasoning, when set, is invoked with each streaming chunk of the model's
	// chain-of-thought (deepseek's reasoning_content), which arrives before the
	// content chunks. Used to surface the thinking to the client.
	OnReasoning func(string)
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON arguments
}

type ChatResponse struct {
	Content    string
	StopReason string
	Usage      *Usage
	ToolCalls  []ToolCall
	// Model is the model actually used for this response.
	Model string
	// Reasoning is the model's full chain-of-thought (deepseek's
	// reasoning_content) for this response, populated when thinking mode is on.
	Reasoning string
}

type LLMProvider interface {
	Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
	Name() string
	Model() string
}
