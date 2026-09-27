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

package history

import (
	"strings"
	"testing"

	"llm-bridge/llm"
)

func TestAppendUser(t *testing.T) {
	var h []llm.Message
	AppendUser(&h, "hello")
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Role != llm.RoleUser || h[0].Content != "hello" {
		t.Fatalf("unexpected message: %+v", h[0])
	}
}

func TestAppendUserWithImages(t *testing.T) {
	var h []llm.Message
	imgs := []llm.Image{{MIME: "image/png", Data: "AAAA"}}
	AppendUserWithImages(&h, "look at this", imgs)
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Role != llm.RoleUser || h[0].Content != "look at this" {
		t.Fatalf("unexpected message: %+v", h[0])
	}
	if len(h[0].Images) != 1 || h[0].Images[0].MIME != "image/png" {
		t.Fatalf("images not preserved: %+v", h[0].Images)
	}
}

func TestAppendAssistant(t *testing.T) {
	var h []llm.Message
	resp := &llm.ChatResponse{Content: "answer", StopReason: "END_TURN"}
	AppendAssistant(&h, resp)
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Role != llm.RoleAssistant || h[0].Content != "answer" {
		t.Fatalf("unexpected message: %+v", h[0])
	}
}

func TestAppendAssistantEmptyDropped(t *testing.T) {
	var h []llm.Message
	resp := &llm.ChatResponse{Content: "", ToolCalls: nil}
	AppendAssistant(&h, resp)
	if len(h) != 0 {
		t.Fatalf("len = %d, want 0", len(h))
	}
}

func TestAppendAssistantStoresReasoning(t *testing.T) {
	var h []llm.Message
	resp := &llm.ChatResponse{
		Content:   "answer",
		Reasoning: "think step by step",
		ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "read"}},
	}
	AppendAssistant(&h, resp)
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Reasoning != "think step by step" {
		t.Fatalf("Reasoning = %q, want %q", h[0].Reasoning, "think step by step")
	}
	if len(h[0].ToolCalls) != 1 || h[0].ToolCalls[0].ID != "call_1" {
		t.Fatalf("tool calls not preserved: %+v", h[0].ToolCalls)
	}
}

// TestAppendAssistantDropsReasoningWithoutToolCalls verifies that reasoning
// (chain-of-thought) is NOT kept in the history for an assistant message that
// performed no tool calls, since it is never re-sent to a provider in that
// case (DeepSeek omits reasoning_content, Gemini has no input channel for it).
func TestAppendAssistantDropsReasoningWithoutToolCalls(t *testing.T) {
	var h []llm.Message
	resp := &llm.ChatResponse{Content: "answer", Reasoning: "think step by step"}
	AppendAssistant(&h, resp)
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Reasoning != "" {
		t.Fatalf("Reasoning = %q, want empty (no tool calls)", h[0].Reasoning)
	}
}

func TestAppendToolResult(t *testing.T) {
	var h []llm.Message
	AppendToolResult(&h, "call_1", `{"ok":true}`)
	if len(h) != 1 {
		t.Fatalf("len = %d, want 1", len(h))
	}
	if h[0].Role != "tool" || h[0].ToolCallID != "call_1" || h[0].Content != `{"ok":true}` {
		t.Fatalf("unexpected message: %+v", h[0])
	}
}

func TestMarkEphemeral(t *testing.T) {
	got := MarkEphemeral("hello")
	if !strings.Contains(got, "hello") ||
		!strings.Contains(got, "<!-- ephemeral -->") ||
		!strings.Contains(got, "<!-- /ephemeral -->") {
		t.Fatalf("MarkEphemeral() = %q, should contain content and both markers", got)
	}
}

func TestSanitizeRemovesEphemeral(t *testing.T) {
	h := []llm.Message{
		{Role: llm.RoleUser, Content: MarkEphemeral("hi")},
		{Role: llm.RoleAssistant, Content: "world"},
	}
	Sanitize(&h)
	if len(h) != 2 {
		t.Fatalf("len = %d, want 2", len(h))
	}
	if h[0].Role != llm.RoleUser || h[0].Content != "" {
		t.Fatalf("ephemeral content should be stripped, got %+v", h[0])
	}
	if h[1].Role != llm.RoleAssistant || h[1].Content != "world" {
		t.Fatalf("unexpected message: %+v", h[1])
	}
}

func TestStripEphemeral(t *testing.T) {
	in := "before <!-- ephemeral --> secret <!-- /ephemeral --> after"
	got := StripEphemeral(in)
	if got != "before  after" {
		t.Fatalf("StripEphemeral() = %q, want %q", got, "before  after")
	}
}

func TestStripEphemeralUnclosed(t *testing.T) {
	in := "start <!-- ephemeral --> rest"
	got := StripEphemeral(in)
	expected := "start  rest"
	if got != expected {
		t.Fatalf("StripEphemeral() = %q, want %q", got, expected)
	}
}

func TestSanitizeRemovesOrphanToolCalls(t *testing.T) {
	h := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "read"}}},
	}
	Sanitize(&h)
	if len(h) != 0 {
		t.Fatalf("len = %d, want 0", len(h))
	}
}

func TestSanitizeKeepsAnsweredToolCalls(t *testing.T) {
	h := []llm.Message{
		{Role: llm.RoleAssistant, ToolCalls: []llm.ToolCall{{ID: "call_1", Name: "read"}}},
		{Role: "tool", ToolCallID: "call_1", Content: `{"ok":true}`},
	}
	Sanitize(&h)
	if len(h) != 2 {
		t.Fatalf("len = %d, want 2", len(h))
	}
}
