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

type Ready struct {
	Event Event `json:"event"`
}

type Chunk struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

// Thinking is a streaming fragment of the model's chain-of-thought
// (reasoning_content), emitted before the content chunks when thinking mode is
// enabled. The client concatenates the Text fragments, mirroring Chunk.
type Thinking struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

type ToolCallEvent struct {
	Event Event           `json:"event"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type TurnEnd struct {
	Event        Event    `json:"event"`
	StopReason   string   `json:"stop_reason"`
	ContextPct   *float64 `json:"context_pct"`
	Model        string   `json:"model,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	TotalTokens  int      `json:"total_tokens"`
	// CacheHitTokens/CacheMissTokens are the prompt cache accounting for the
	// whole turn, summed over every provider call made in it (the turn may
	// include several tool-cycle round trips). Providers without cache
	// reporting leave them at zero.
	CacheHitTokens  int `json:"cache_hit_tokens"`
	CacheMissTokens int `json:"cache_miss_tokens"`
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

// FilesChanged reports the paths that were written or modified during the
// just-completed turn (via the write and search_replace tools). Reads are not
// included. The client can use it to refresh buffers/status for those files.
type FilesChanged struct {
	Event Event    `json:"event"`
	Files []string `json:"files"`
}

func NewReady() []byte {
	evt := Ready{Event: EventReady}
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

func NewTurnEnd(stopReason string, contextPct *float64, model string, inputTokens, outputTokens, totalTokens, cacheHitTokens, cacheMissTokens int) []byte {
	evt := TurnEnd{Event: EventTurnEnd, StopReason: stopReason,
		ContextPct: contextPct, Model: model,
		InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens,
		CacheHitTokens: cacheHitTokens, CacheMissTokens: cacheMissTokens}
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

// NewFilesChanged serializes a files_changed event for the given modified file
// paths. An empty list is allowed (the client can just ignore the event).
func NewFilesChanged(files []string) []byte {
	evt := FilesChanged{Event: EventFilesChanged, Files: files}
	b, _ := json.Marshal(evt)
	return b
}

// HookEvent reports the result of running a local hook script (triggered by a
// message starting with "#"). Output carries the script's combined
// stdout+stderr on success; Error carries the failure reason when the hook
// could not be run or exited non-zero. Exactly one of Output/Error is set.
type HookEvent struct {
	Event  Event  `json:"event"`
	Name   string `json:"name"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewHook serializes a hook_action event for the given hook name. When errMsg
// is non-empty it is reported in the Error field (Output is omitted);
// otherwise output carries the script's result.
func NewHook(name, output, errMsg string) []byte {
	evt := HookEvent{Event: EventHookAction, Name: name, Output: output, Error: errMsg}
	b, _ := json.Marshal(evt)
	return b
}
