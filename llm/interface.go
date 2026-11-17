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

// StopReason values are a common vocabulary for why a turn ended. Providers
// map their own strings onto these, so a client sees the same words whichever
// provider answered.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// ReasoningLevel is the provider-independent effort scale. Each provider maps
// it onto whatever it accepts: DeepSeek sends it as reasoning_effort, Gemini
// turns it into a token budget, Anthropic sends it as output_config.effort.
type ReasoningLevel string

const (
	ReasoningLow    ReasoningLevel = "low"
	ReasoningMedium ReasoningLevel = "medium"
	ReasoningHigh   ReasoningLevel = "high"
	ReasoningXHigh  ReasoningLevel = "xhigh"
	ReasoningMax    ReasoningLevel = "max"
)

// ValidReasoningLevel reports whether s names a level on this scale.
func ValidReasoningLevel(s string) bool {
	switch ReasoningLevel(s) {
	case ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax:
		return true
	}
	return false
}

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
	// Signature is the provider's proof that Reasoning came from it: Anthropic
	// signs the thinking block and rejects the block back without the
	// signature. Gemini signs the tool call instead (ToolCall.Signature), and
	// DeepSeek signs nothing.
	Signature string
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
	// ReasoningLevel overrides the effort on the common scale. nil keeps the
	// provider's configured value; a level the provider does not know falls
	// back to its default.
	ReasoningLevel *ReasoningLevel
	// ThinkingBudget asks for a reasoning budget in tokens, for a client that
	// wants a number instead of a level. 0 keeps the provider's configured
	// value, and providers without a numeric budget ignore it.
	ThinkingBudget int
	// OnReasoning receives each chunk of the chain-of-thought, which streams
	// before the content.
	OnReasoning func(string)
}

type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
	// CachedTokens is the prompt tokens a provider served from its cache. The
	// uncached remainder is PromptTokens - CachedTokens. Providers without a
	// prompt cache leave it at zero.
	CachedTokens int
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string // raw JSON arguments
	// Signature is the provider's proof for this call. Gemini signs the
	// function call and rejects a replayed history without it; Anthropic signs
	// the thinking block instead (Message.Signature). Empty elsewhere.
	Signature string
}

type ChatResponse struct {
	Content string
	// StopReason is one of the Stop* constants when the provider reported one,
	// or the provider's own string when it is outside the common vocabulary.
	StopReason string
	Usage      *Usage
	ToolCalls  []ToolCall
	// Model is the model actually used for this response.
	Model string
	// Reasoning is the full chain-of-thought, set when thinking is on.
	Reasoning string
	// Signature is the provider's proof for Reasoning, replayed as-is.
	Signature string
}

type LLMProvider interface {
	Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error)
	Name() string
	Model() string
}
