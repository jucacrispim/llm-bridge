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
	// Images are optional attachments on a user message. deepseek and Gemini
	// both reject images on a system or assistant message.
	Images []Image
	// Reasoning is the chain-of-thought that came with this message. It goes
	// back to the API on assistant messages that made tool calls, where
	// deepseek answers 400 without it, and is dropped from plain answers.
	Reasoning string
}

type Tool struct {
	Name        string
	Description string
	Parameters  json.RawMessage
	// ReadOnly marks a tool the bridge runs without confirmation. A mutating one
	// goes to the client first. This is a classification only: providers build
	// their own payload and never see it.
	ReadOnly bool
}

type ChatRequest struct {
	Messages []Message
	Tools    []Tool
	// System is the system prompt: a system message for OpenAI-compatible
	// providers, system_instruction for Gemini. It is not in the history, so it
	// goes out on every request.
	System string
	Model  string
	// Thinking overrides the provider's thinking mode for this request. nil
	// keeps the configured mode; true/false switches between deepseek-flash and
	// deepseek-chat unless Model is set.
	Thinking *bool
	// ReasoningEffort overrides the provider's reasoning_effort when thinking is
	// on. nil keeps the configured value, "" stops sending the parameter.
	ReasoningEffort *string
	// OnReasoning receives each chunk of the chain-of-thought, which streams
	// before the content.
	OnReasoning func(string)
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheHitTokens/CacheMissTokens are the provider's prompt cache accounting:
	// DeepSeek's prompt_cache_hit/miss_tokens, Google's cachedContentTokenCount.
	CacheHitTokens  int `json:"prompt_cache_hit_tokens,omitempty"`
	CacheMissTokens int `json:"prompt_cache_miss_tokens,omitempty"`
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON arguments
	// ThoughtSignature is Gemini's base64 thought_signature, echoed back on the
	// functionCall part when thinking is on, so a call from an earlier turn can
	// be re-sent. Empty for deepseek.
	ThoughtSignature string
}

type ChatResponse struct {
	Content    string
	StopReason string
	Usage      *Usage
	ToolCalls  []ToolCall
	// Model is the model actually used for this response.
	Model string
	// Reasoning is the full chain-of-thought, set when thinking is on.
	Reasoning string
}

type LLMProvider interface {
	Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
	Name() string
	Model() string
}
