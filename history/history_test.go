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
	got := MarkEphemeral("olá")
	if !strings.Contains(got, "olá") ||
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
	in := "antes <!-- ephemeral --> segredo <!-- /ephemeral --> depois"
	got := StripEphemeral(in)
	if got != "antes  depois" {
		t.Fatalf("StripEphemeral() = %q, want %q", got, "antes  depois")
	}
}

func TestStripEphemeralUnclosed(t *testing.T) {
	in := "início <!-- ephemeral --> resto"
	got := StripEphemeral(in)
	expected := "início  resto"
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
