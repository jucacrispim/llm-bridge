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

package protocol

import "encoding/json"

type InboundCommand struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func ParseInbound(data []byte) (*InboundCommand, error) {
	var cmd InboundCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		return nil, err
	}
	return &cmd, nil
}

// Ready is the first event of a session.
type Ready struct {
	Event Event `json:"event"`
	// DefaultProvider is the provider a prompt without a provider override
	// uses.
	DefaultProvider string `json:"default_provider,omitempty"`
	// Providers is what the bridge can be asked for, sorted by name. A client
	// uses it to offer the real provider and model names instead of keeping its
	// own copy of them.
	Providers []ProviderInfo `json:"providers,omitempty"`
}

// ProviderInfo is one provider the bridge knows how to reach.
type ProviderInfo struct {
	Name string `json:"name"`
	// Model is the model in use now, before any per-prompt override.
	Model string `json:"model"`
	// Models are the ids known for this provider. A prompt may still name any
	// model: the list is what to offer, not an allow list.
	Models []ModelInfo `json:"models,omitempty"`
	// Reasoning is how the provider takes the reasoning effort, absent for a
	// provider the bridge does not know.
	Reasoning *ReasoningInfo `json:"reasoning,omitempty"`
}

// ModelInfo is a model the bridge knows, with the window it reports in
// `turn_end`.
type ModelInfo struct {
	ID     string `json:"id"`
	Window int    `json:"window"`
}

// ReasoningInfo is how a provider takes the reasoning effort.
type ReasoningInfo struct {
	// Levels are the effort levels the provider accepts, ascending.
	Levels []string `json:"levels,omitempty"`
	// Budget reports whether a token budget works instead of a level.
	Budget bool `json:"budget"`
}

type Chunk struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

// Thinking is a fragment of the chain-of-thought, emitted before the content
// chunks. The client concatenates the Text fragments, like Chunk.
type Thinking struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

type ToolCallEvent struct {
	Event Event           `json:"event"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`

	// Segments, Risk and Tainted are only set on `tool_confirm` events, and an
	// older client ignores them. One approval can run far more than it looks
	// like: Segments lists the commands the one-liner runs, Risk labels a
	// destructive, remote or privileged one, and Tainted marks arguments that
	// reproduce content fetched earlier in the session. All three are advisory.
	Segments []string `json:"segments,omitempty"`
	Risk     string   `json:"risk,omitempty"`
	Tainted  bool     `json:"tainted,omitempty"`
}

type TurnEnd struct {
	Event      Event    `json:"event"`
	StopReason string   `json:"stop_reason"`
	ContextPct *float64 `json:"context_pct"`
	// ContextTokens is the size of the prompt sent on the last provider call of
	// the turn, and ContextWindow is that model's static window. ContextPct is
	// their ratio, or null when the window is unknown.
	ContextTokens int    `json:"context_tokens"`
	ContextWindow int    `json:"context_window"`
	Model         string `json:"model,omitempty"`
	InputTokens   int    `json:"input_tokens"`
	OutputTokens  int    `json:"output_tokens"`
	TotalTokens   int    `json:"total_tokens"`
	// CacheHitTokens/CacheMissTokens sum the cache accounting over every
	// provider call in the turn. Providers without cache reporting leave them
	// at zero.
	CacheHitTokens  int `json:"cache_hit_tokens"`
	CacheMissTokens int `json:"cache_miss_tokens"`
	// DurationMS is the wall time of the turn, from the prompt to this event.
	// LLMMS and ToolMS are the parts spent in provider calls and in tool
	// execution. The wait for a tool_confirm is in none of them.
	DurationMS int64 `json:"duration_ms"`
	LLMMS      int64 `json:"llm_ms"`
	ToolMS     int64 `json:"tool_ms"`
	// CostUSD is the cost of the turn and SessionCostUSD the running total of
	// the session. A turn with a call the provider could not price reports null.
	CostUSD        *float64 `json:"cost_usd"`
	SessionCostUSD *float64 `json:"session_cost_usd"`
}

type ErrorEvent struct {
	Event   Event  `json:"event"`
	Message string `json:"message"`
}

type Cancelled struct {
	Event Event `json:"event"`
}

type UsageDelta struct {
	Event        Event `json:"event"`
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	TotalTokens  int   `json:"total_tokens"`
}

// FilesChanged reports the paths written during the turn (write and
// search_replace; reads are not included), so a client can refresh them.
type FilesChanged struct {
	Event Event    `json:"event"`
	Files []string `json:"files"`
}

// NewReady serializes the `ready` event. An empty defaultProvider and provider
// list give the bare `{"event":"ready"}` an older client expects.
func NewReady(defaultProvider string, providers []ProviderInfo) []byte {
	evt := Ready{Event: EventReady, DefaultProvider: defaultProvider, Providers: providers}
	b, _ := json.Marshal(evt)
	return b
}

func NewChunk(text string) []byte {
	evt := Chunk{Event: EventChunk, Text: text}
	b, _ := json.Marshal(evt)
	return b
}

func NewThinking(text string) []byte {
	evt := Thinking{Event: EventThinking, Text: text}
	b, _ := json.Marshal(evt)
	return b
}

func NewToolCall(id, name string, input any) ([]byte, error) {
	rawInput, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	evt := ToolCallEvent{Event: EventToolCall, ID: id, Name: name, Input: rawInput}
	return json.Marshal(evt)
}

// ToolOutput is one fragment of a tool that streams its result.
type ToolOutput struct {
	Event Event  `json:"event"`
	ID    string `json:"id"`
	Name  string `json:"name"`
	Text  string `json:"text"`
}

// NewToolOutput serializes a `tool_output` event: the fragment a running tool
// printed, already accumulated in the history by the time the turn ends.
func NewToolOutput(id, name, text string) []byte {
	evt := ToolOutput{Event: EventToolOutput, ID: id, Name: name, Text: text}
	b, _ := json.Marshal(evt)
	return b
}

// ConfirmMeta carries the detail a mutating call is offered with: the commands
// a one-liner runs, a coarse risk label and whether the arguments quote content
// fetched earlier. All three are advisory and optional on the wire.
type ConfirmMeta struct {
	Segments []string
	Risk     string
	Tainted  bool
}

// NewToolConfirm serializes a `tool_confirm` event, the tool_call shape with
// the tool_confirm name.
func NewToolConfirm(id, name string, input any) ([]byte, error) {
	return NewToolConfirmWithMeta(id, name, input, ConfirmMeta{})
}

// NewToolConfirmWithMeta is NewToolConfirm plus the detail shown to the human.
func NewToolConfirmWithMeta(id, name string, input any, meta ConfirmMeta) ([]byte, error) {
	rawInput, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	evt := ToolCallEvent{Event: EventToolConfirm, ID: id, Name: name, Input: rawInput,
		Segments: meta.Segments, Risk: meta.Risk, Tainted: meta.Tainted}
	return json.Marshal(evt)
}

func NewTurnEnd(stopReason string, contextPct *float64, model string, inputTokens, outputTokens, totalTokens, cacheHitTokens, cacheMissTokens, contextTokens, contextWindow int, durationMS, llmMS, toolMS int64, costUSD, sessionCostUSD *float64) []byte {
	evt := TurnEnd{Event: EventTurnEnd, StopReason: stopReason,
		ContextPct: contextPct, ContextTokens: contextTokens, ContextWindow: contextWindow,
		Model:       model,
		InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens,
		CacheHitTokens: cacheHitTokens, CacheMissTokens: cacheMissTokens,
		DurationMS: durationMS, LLMMS: llmMS, ToolMS: toolMS,
		CostUSD: costUSD, SessionCostUSD: sessionCostUSD}
	b, _ := json.Marshal(evt)
	return b
}

func NewError(message string) []byte {
	evt := ErrorEvent{Event: EventError, Message: message}
	b, _ := json.Marshal(evt)
	return b
}

func NewCancelled() []byte {
	evt := Cancelled{Event: EventCancelled}
	b, _ := json.Marshal(evt)
	return b
}

func NewUsageDelta(inputTokens, outputTokens, totalTokens int) []byte {
	evt := UsageDelta{Event: EventUsageDelta, InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}
	b, _ := json.Marshal(evt)
	return b
}

// NewFilesChanged serializes a files_changed event. An empty list is allowed.
func NewFilesChanged(files []string) []byte {
	evt := FilesChanged{Event: EventFilesChanged, Files: files}
	b, _ := json.Marshal(evt)
	return b
}

// HookEvent reports the result of a hook script: Output carries its combined
// stdout+stderr, Error the reason it could not run or exited non-zero. Exactly
// one is set.
type HookEvent struct {
	Event  Event  `json:"event"`
	Name   string `json:"name"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewHook serializes a hook_action event. A non-empty errMsg goes in Error and
// drops Output.
func NewHook(name, output, errMsg string) []byte {
	evt := HookEvent{Event: EventHookAction, Name: name, Output: output, Error: errMsg}
	b, _ := json.Marshal(evt)
	return b
}
