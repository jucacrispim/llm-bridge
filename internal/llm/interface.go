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
	Role    string
	Content string
	ToolCalls []ToolCall
	ToolCallID string
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
}

type LLMProvider interface {
	Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
	Name() string
	Model() string
}
