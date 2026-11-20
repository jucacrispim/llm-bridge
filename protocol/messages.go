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

type Method string

const (
	MethodPrompt            Method = "prompt"
	MethodCancel            Method = "cancel"
	MethodSetCwd            Method = "set_cwd"
	MethodSetKnowledgeBases Method = "set_knowledge_bases"
	MethodToolResult        Method = "tool_result"
	// MethodToolConfirm approves a mutating tool call offered via a
	// `tool_confirm` event. A denied tool is signalled with `cancel`.
	MethodToolConfirm Method = "tool_confirm"
	MethodQuit        Method = "quit"
)

type Event string

const (
	EventReady    Event = "ready"
	EventChunk    Event = "chunk"
	EventThinking Event = "thinking"
	EventToolCall Event = "tool_call"
	// EventToolConfirm asks the client to approve a mutating call before the
	// bridge runs it. Read-only tools come as `tool_call`, already executed.
	EventToolConfirm Event = "tool_confirm"
	// EventToolOutput carries a fragment of a running tool's output, so the
	// client can show it as it arrives.
	EventToolOutput   Event = "tool_output"
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
	// Provider switches the LLM provider for this and subsequent prompts (e.g.
	// "deepseek" or "google"). Omitted keeps the current one, like the other
	// overrides.
	Provider string `json:"provider,omitempty"`
	// KBScope switches which knowledge base(s) are used for this and subsequent
	// prompts: "global", "project" or "both". Omitted keeps -kb-scope.
	KBScope string `json:"kb_scope,omitempty"`
	// Thinking overrides thinking mode for the conversation: true →
	// deepseek-flash, false → deepseek-chat. Omitted keeps the configured mode.
	Thinking *bool `json:"thinking,omitempty"`
	// ReasoningEffort overrides the reasoning effort when thinking is on: a word
	// on the common scale ("low", "medium", "high", "xhigh", "max") or a number,
	// which becomes a token budget. "" stops sending the parameter.
	ReasoningEffort *string `json:"reasoning_effort,omitempty"`
	// System overrides the system prompt, and persists like the other
	// overrides.
	System string `json:"system,omitempty"`
	// SystemPrompt is an alias for System.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// Images are optional attachments: a local path, an http(s) url or inline
	// base64.
	Images []ImageParam `json:"images,omitempty"`
}

// ImageParam is one image attached to a prompt, resolved by the bridge into the
// active provider's wire format.
type ImageParam struct {
	// Path is a local file, absolute or relative to the cwd.
	Path string `json:"path,omitempty"`
	// URL is an http(s) image link, passed through to the provider.
	URL string `json:"url,omitempty"`
	// Data is inline base64, raw or as a full data: URL.
	Data string `json:"data,omitempty"`
	// MIME declares the media type; otherwise it is detected from the bytes.
	MIME string `json:"mime_type,omitempty"`
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
