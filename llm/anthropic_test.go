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
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// anthropicTestServer serves the given SSE events and records the request body.
func anthropicTestServer(t *testing.T, events []string) (*httptest.Server, *string) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			_, _ = w.Write([]byte(e + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

func TestNewAnthropicProviderFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ANTHROPIC_URL", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	t.Setenv("ANTHROPIC_VERSION", "")
	t.Setenv("ANTHROPIC_MAX_TOKENS", "")
	t.Setenv("ANTHROPIC_THINKING", "")
	t.Setenv("ANTHROPIC_THINKING_BUDGET", "")

	p := NewAnthropicProviderFromEnv()
	if p.Name() != "anthropic" {
		t.Errorf("Name() = %q, want anthropic", p.Name())
	}
	if p.Model() != DefaultAnthropicModel {
		t.Errorf("Model() = %q, want %q", p.Model(), DefaultAnthropicModel)
	}
	if p.Version() != DefaultAnthropicVersion {
		t.Errorf("Version() = %q, want %q", p.Version(), DefaultAnthropicVersion)
	}
	if p.MaxTokens() != DefaultAnthropicMaxTokens {
		t.Errorf("MaxTokens() = %d, want %d", p.MaxTokens(), DefaultAnthropicMaxTokens)
	}
	// No budget configured means adaptive thinking, not manual extended
	// thinking.
	if !p.Thinking() || p.ThinkingBudget() != 0 {
		t.Errorf("thinking = %v budget = %d, want true and 0", p.Thinking(), p.ThinkingBudget())
	}
}

func TestNewAnthropicProviderFromEnvCustom(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ANTHROPIC_URL", "https://example.com/messages")
	t.Setenv("ANTHROPIC_MODEL", "claude-haiku-5-5")
	t.Setenv("ANTHROPIC_VERSION", "2030-01-01")
	t.Setenv("ANTHROPIC_MAX_TOKENS", "2048")
	t.Setenv("ANTHROPIC_THINKING", "false")
	t.Setenv("ANTHROPIC_THINKING_BUDGET", "4096")

	p := NewAnthropicProviderFromEnv()
	if p.endpoint != "https://example.com/messages" {
		t.Errorf("endpoint = %q", p.endpoint)
	}
	if p.Model() != "claude-haiku-5-5" {
		t.Errorf("Model() = %q", p.Model())
	}
	if p.Version() != "2030-01-01" {
		t.Errorf("Version() = %q", p.Version())
	}
	if p.MaxTokens() != 2048 {
		t.Errorf("MaxTokens() = %d", p.MaxTokens())
	}
	if p.Thinking() {
		t.Error("Thinking() = true, want false")
	}
	if p.ThinkingBudget() != 4096 {
		t.Errorf("ThinkingBudget() = %d, want 4096", p.ThinkingBudget())
	}
}

func TestAnthropicChatRequestPayload(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":5}}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		System:         "be brief",
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		Thinking:       boolPtr(true),
		ThinkingBudget: 4096,
		Tools: []Tool{{
			Name:        "read",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`),
		}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var got struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
		System    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"system"`
		Stream   bool `json:"stream"`
		Thinking struct {
			Type   string `json:"type"`
			Budget int    `json:"budget_tokens"`
		} `json:"thinking"`
		Tools []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			InputSchema json.RawMessage `json:"input_schema"`
		} `json:"tools"`
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(*lastBody), &got); err != nil {
		t.Fatalf("unmarshal request: %v (%s)", err, *lastBody)
	}
	if got.Model != "claude-sonnet-5-5" || !got.Stream {
		t.Errorf("model=%q stream=%v", got.Model, got.Stream)
	}
	if len(got.System) != 1 || got.System[0].Text != "be brief" {
		t.Errorf("system = %+v", got.System)
	}
	if got.MaxTokens <= 4096 {
		t.Errorf("max_tokens = %d, want more than the 4096 budget", got.MaxTokens)
	}
	if got.Thinking.Type != "enabled" || got.Thinking.Budget != 4096 {
		t.Errorf("thinking = %+v, want enabled with a 4096 budget", got.Thinking)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "read" ||
		!strings.Contains(string(got.Tools[0].InputSchema), `"path"`) {
		t.Errorf("tools = %+v", got.Tools)
	}
	if len(got.Messages) != 1 || got.Messages[0].Role != RoleUser ||
		got.Messages[0].Content[0].Text != "hi" {
		t.Errorf("messages = %+v", got.Messages)
	}
}

// cacheBreakpoints counts the cache_control marks of a request body. The API
// accepts four.
func cacheBreakpoints(body string) int {
	return strings.Count(body, `"cache_control"`)
}

func TestAnthropicCacheBreakpoints(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, []string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5-5"}}`,
		`data: {"type":"message_stop"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "")
	tools := make([]Tool, 0, 3)
	for _, name := range []string{"read", "write", "shell"} {
		tools = append(tools, Tool{
			Name:       name,
			Parameters: json.RawMessage(`{"type":"object"}`),
		})
	}
	_, err := p.Chat(context.Background(), ChatRequest{
		System:   "be brief",
		Tools:    tools,
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	body := *lastBody
	if n := cacheBreakpoints(body); n != 3 {
		t.Errorf("%d breakpoints, want 3 (tools, system, last message): %s", n, body)
	}
	if !strings.Contains(body, `"system":[{"type":"text","cache_control":{"type":"ephemeral"},"text":"be brief"}]`) {
		t.Errorf("system block has no breakpoint: %s", body)
	}
	// The breakpoint sits on the last tool, so the tool list is one prefix.
	if strings.Contains(body, `"name":"read","input_schema":{"type":"object"},"cache_control"`) {
		t.Errorf("first tool carries the breakpoint: %s", body)
	}
	if !strings.Contains(body, `"name":"shell","input_schema":{"type":"object"},"cache_control"`) {
		t.Errorf("last tool carries no breakpoint: %s", body)
	}
}

func TestAnthropicCacheBreakpointOnToolResult(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, []string{
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5-5"}}`,
		`data: {"type":"message_stop"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "")
	_, err := p.Chat(context.Background(), ChatRequest{
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Content: "read the file"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "1", Name: "read", Arguments: `{"path":"x"}`},
				{ID: "2", Name: "read", Arguments: `{"path":"y"}`},
			}},
			{Role: "tool", ToolCallID: "1", Content: "a"},
			{Role: "tool", ToolCallID: "2", Content: "b"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	// The two results share one user message, and the breakpoint goes on its
	// last block.
	if n := cacheBreakpoints(*lastBody); n != 2 {
		t.Errorf("%d breakpoints, want 2 (system, last tool result): %s", n, *lastBody)
	}
	if !strings.Contains(*lastBody, `"tool_result","cache_control":{"type":"ephemeral"},"tool_use_id":"2"`) {
		t.Errorf("last tool result carries no breakpoint: %s", *lastBody)
	}
}

func TestAnthropicCacheBreakpointSkipsThinking(t *testing.T) {
	cases := []struct {
		name     string
		messages []anthropicMessage
		want     []bool
	}{
		{
			name: "no messages",
		},
		{
			name: "only thinking blocks",
			messages: []anthropicMessage{{
				Role: RoleAssistant,
				Content: []anthropicBlock{
					{Type: "thinking", Thinking: "hmm", Signature: "sig"},
					{Type: "redacted_thinking", Data: "xyz"},
				},
			}},
			want: []bool{false, false},
		},
		{
			name: "thinking before text",
			messages: []anthropicMessage{{
				Role: RoleAssistant,
				Content: []anthropicBlock{
					{Type: "thinking", Thinking: "hmm", Signature: "sig"},
					{Type: "text", Text: "answer"},
				},
			}},
			want: []bool{false, true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			markMessageBreakpoint(tc.messages)
			if len(tc.want) == 0 {
				return
			}
			for i, want := range tc.want {
				got := tc.messages[0].Content[i].CacheControl != nil
				if got != want {
					t.Errorf("block %d marked = %v, want %v", i, got, want)
				}
			}
		})
	}
}

func TestAnthropicHeaders(t *testing.T) {
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		w.Header().Set("Content-Type", "text/event-stream")
	}))
	defer srv.Close()

	p := NewAnthropicProvider("secret", srv.URL, "claude-sonnet-5-5")
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if gotKey != "secret" {
		t.Errorf("x-api-key = %q", gotKey)
	}
	if gotVersion != DefaultAnthropicVersion {
		t.Errorf("anthropic-version = %q, want %q", gotVersion, DefaultAnthropicVersion)
	}
}

func TestAnthropicThinkingAdaptiveAndDisabled(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")

	// Thinking on with no budget is adaptive.
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Thinking: boolPtr(true),
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"thinking":{"type":"adaptive","display":"summarized"}`) {
		t.Errorf("expected adaptive thinking, got %s", *lastBody)
	}

	// Thinking off sends the disabled form.
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Thinking: boolPtr(false),
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"thinking":{"type":"disabled"}`) {
		t.Errorf("expected disabled thinking, got %s", *lastBody)
	}
}

func TestAnthropicEffortIsOutputConfig(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	level := ReasoningXHigh
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: &level,
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"output_config":{"effort":"xhigh"}`) {
		t.Errorf("expected output_config effort, got %s", *lastBody)
	}
}

func TestAnthropicToolResultGrouping(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "read two files"},
			{Role: RoleAssistant, ToolCalls: []ToolCall{
				{ID: "toolu_1", Name: "read", Arguments: `{"path":"a.txt"}`},
				{ID: "toolu_2", Name: "read", Arguments: `{"path":"b.txt"}`},
			}},
			{Role: "tool", Content: "contents of a", ToolCallID: "toolu_1"},
			{Role: "tool", Content: "contents of b", ToolCallID: "toolu_2"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				ID        string `json:"id"`
				Name      string `json:"name"`
				Input     any    `json:"input"`
				ToolUseID string `json:"tool_use_id"`
				Content   string `json:"content"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(*lastBody), &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3 (user, assistant, user with both results)", len(got.Messages))
	}
	assistant := got.Messages[1]
	if assistant.Role != RoleAssistant || len(assistant.Content) != 2 {
		t.Fatalf("assistant = %+v", assistant)
	}
	if assistant.Content[0].Type != "tool_use" || assistant.Content[0].ID != "toolu_1" {
		t.Errorf("first tool_use = %+v", assistant.Content[0])
	}
	last := got.Messages[2]
	if last.Role != RoleUser || len(last.Content) != 2 {
		t.Fatalf("tool results = %+v, want one user message with two blocks", last)
	}
	for i, want := range []string{"toolu_1", "toolu_2"} {
		if last.Content[i].Type != "tool_result" || last.Content[i].ToolUseID != want {
			t.Errorf("tool_result %d = %+v", i, last.Content[i])
		}
	}
}

func TestAnthropicReasoningGoesBackWithSignature(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "read a.txt"},
			{
				Role:      RoleAssistant,
				Reasoning: "I should read the file",
				Signature: "sig-abc",
				ToolCalls: []ToolCall{{ID: "toolu_1", Name: "read", Arguments: `{"path":"a.txt"}`}},
			},
			{Role: "tool", Content: "hello", ToolCallID: "toolu_1"},
		},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	var got struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type      string `json:"type"`
				Thinking  string `json:"thinking"`
				Signature string `json:"signature"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(*lastBody), &got); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	blocks := got.Messages[1].Content
	if len(blocks) != 2 || blocks[0].Type != "thinking" {
		t.Fatalf("assistant blocks = %+v, want the thinking block first", blocks)
	}
	if blocks[0].Thinking != "I should read the file" || blocks[0].Signature != "sig-abc" {
		t.Errorf("thinking block = %+v", blocks[0])
	}
	if blocks[1].Type != "tool_use" {
		t.Errorf("second block = %+v, want tool_use", blocks[1])
	}
}

func TestAnthropicImages(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "look", Images: []Image{
			{MIME: "image/png", Data: "AAAA"},
			{URL: "https://example.com/x.jpg"},
		}}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}`) {
		t.Errorf("expected the inline image block, got %s", *lastBody)
	}
	if !strings.Contains(*lastBody, `{"type":"image","source":{"type":"url","url":"https://example.com/x.jpg"}}`) {
		t.Errorf("expected the url image block, got %s", *lastBody)
	}
}

func TestAnthropicChatStreamsTextThinkingAndToolUse(t *testing.T) {
	srv, _ := anthropicTestServer(t, []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"model":"claude-sonnet-5-5","usage":{"input_tokens":12,"cache_read_input_tokens":30,"cache_creation_input_tokens":0,"output_tokens":1}}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig-1"}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}`,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a.txt\"}"}}`,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":24}}`,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")

	var chunks []string
	var reasoning []string
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(s string) { reasoning = append(reasoning, s) },
	}, func(s string) { chunks = append(chunks, s) })
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "Hello" {
		t.Errorf("Content = %q", resp.Content)
	}
	if len(chunks) != 1 || chunks[0] != "Hello" {
		t.Errorf("chunks = %v", chunks)
	}
	if resp.Reasoning != "hmm" || len(reasoning) != 1 || reasoning[0] != "hmm" {
		t.Errorf("reasoning = %q, streamed %v", resp.Reasoning, reasoning)
	}
	if resp.Signature != "sig-1" {
		t.Errorf("Signature = %q, want sig-1", resp.Signature)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls = %+v", resp.ToolCalls)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "read" || tc.Arguments != `{"path":"a.txt"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
	if resp.Usage == nil {
		t.Fatal("nil usage")
	}
	// The prompt is the uncached input plus both cache counters.
	if resp.Usage.PromptTokens != 42 || resp.Usage.CachedTokens != 30 {
		t.Errorf("Usage = %+v, want prompt=42 cached=30", resp.Usage)
	}
	if resp.Usage.CompletionTokens != 24 || resp.Usage.TotalTokens != 66 {
		t.Errorf("Usage = %+v, want completion=24 total=66", resp.Usage)
	}
	if resp.Model != "claude-sonnet-5-5" {
		t.Errorf("Model = %q", resp.Model)
	}
}

func TestAnthropicChatErrorEvent(t *testing.T) {
	srv, _ := anthropicTestServer(t, []string{
		`event: error`,
		`data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Fatalf("expected the streamed error, got %v", err)
	}
}

func TestAnthropicChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	}))
	defer srv.Close()

	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("expected an HTTP 400 error, got %v", err)
	}
}

func TestAnthropicStopReasons(t *testing.T) {
	cases := map[string]string{
		"end_turn":      StopEndTurn,
		"tool_use":      StopToolUse,
		"max_tokens":    StopMaxTokens,
		"stop_sequence": "stop_sequence",
		"refusal":       "refusal",
	}
	for raw, want := range cases {
		if got := anthropicStopReason(raw); got != want {
			t.Errorf("anthropicStopReason(%q) = %q, want %q", raw, got, want)
		}
	}
}

// boolPtr is a small helper for the *bool thinking override.
func boolPtr(v bool) *bool { return &v }

func TestAnthropicSetters(t *testing.T) {
	p := NewAnthropicProvider("key", "http://x", "claude-sonnet-5-5")

	p.SetThinking(false)
	if p.Thinking() {
		t.Error("Thinking() = true after SetThinking(false)")
	}
	p.SetThinkingBudget(2048)
	if p.ThinkingBudget() != 2048 {
		t.Errorf("ThinkingBudget() = %d, want 2048", p.ThinkingBudget())
	}
	p.SetVersion("2030-01-01")
	if p.Version() != "2030-01-01" {
		t.Errorf("Version() = %q", p.Version())
	}
	p.SetMaxTokens(1024)
	if p.MaxTokens() != 1024 {
		t.Errorf("MaxTokens() = %d, want 1024", p.MaxTokens())
	}
	p.SetEffort("medium")
	if p.Effort() != "medium" {
		t.Errorf("Effort() = %q", p.Effort())
	}

	// An empty version and a non-positive max_tokens are ignored: both have a
	// value the API accepts, and clearing either would make every request fail.
	p.SetVersion("")
	if p.Version() != "2030-01-01" {
		t.Errorf("Version() = %q after an empty SetVersion", p.Version())
	}
	p.SetMaxTokens(0)
	p.SetMaxTokens(-1)
	if p.MaxTokens() != 1024 {
		t.Errorf("MaxTokens() = %d after a non-positive value", p.MaxTokens())
	}
}

func TestAnthropicModelOverride(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
		Model:    "claude-opus-4-6",
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"model":"claude-opus-4-6"`) {
		t.Errorf("expected the per-request model, got %s", *lastBody)
	}
}

func TestAnthropicConfiguredEffort(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")

	// An effort set on the provider goes out when the request asks for no level.
	p.SetEffort("medium")
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"output_config":{"effort":"medium"}`) {
		t.Errorf("expected the configured effort, got %s", *lastBody)
	}

	// A per-request level wins over it.
	level := ReasoningMax
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ReasoningLevel: &level,
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"output_config":{"effort":"max"}`) {
		t.Errorf("expected the per-request effort, got %s", *lastBody)
	}

	// An empty effort stops sending the parameter.
	p.SetEffort("")
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if strings.Contains(*lastBody, "output_config") {
		t.Errorf("expected no output_config, got %s", *lastBody)
	}
}

// TestAnthropicThinkingBudgetFloorAndMaxTokens checks the two numbers the API
// constrains: budget_tokens has a floor, and max_tokens has to stay above it.
func TestAnthropicThinkingBudgetFloorAndMaxTokens(t *testing.T) {
	srv, lastBody := anthropicTestServer(t, nil)
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")

	// Below the floor the budget is raised to it, and 1024 still fits under the
	// default max_tokens.
	p.SetThinkingBudget(100)
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"budget_tokens":1024`) {
		t.Errorf("expected the budget raised to the floor, got %s", *lastBody)
	}
	if !strings.Contains(*lastBody, `"max_tokens":8192`) {
		t.Errorf("expected the default max_tokens, got %s", *lastBody)
	}

	// A budget above max_tokens pushes max_tokens up, because the API requires
	// the budget to be smaller.
	p.SetThinkingBudget(20000)
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"max_tokens":28192`) {
		t.Errorf("expected max_tokens raised above the budget, got %s", *lastBody)
	}

	// A per-request budget wins over the configured one.
	p.SetThinkingBudget(2048)
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages:       []Message{{Role: RoleUser, Content: "hi"}},
		ThinkingBudget: 4096,
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !strings.Contains(*lastBody, `"budget_tokens":4096`) {
		t.Errorf("expected the per-request budget, got %s", *lastBody)
	}
}

func TestAnthropicToolJSON(t *testing.T) {
	cases := map[string]string{
		"":             "{}",
		"   ":          "{}",
		`{"path":"a"}`: `{"path":"a"}`,
	}
	for in, want := range cases {
		if got := string(anthropicToolJSON(in)); got != want {
			t.Errorf("anthropicToolJSON(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAnthropicTextContent(t *testing.T) {
	if got := string(anthropicTextContent("")); got != `""` {
		t.Errorf(`anthropicTextContent("") = %s, want ""`, got)
	}
	// A result with quotes and newlines has to survive as a JSON string.
	got := string(anthropicTextContent("a \"quoted\"\nline"))
	var back string
	if err := json.Unmarshal([]byte(got), &back); err != nil {
		t.Fatalf("unmarshal %s: %v", got, err)
	}
	if back != "a \"quoted\"\nline" {
		t.Errorf("round trip = %q", back)
	}
}

func TestBuildAnthropicMessages(t *testing.T) {
	p := NewAnthropicProvider("key", "http://x", "claude-sonnet-5-5")
	msgs := p.buildAnthropicMessages([]Message{
		{Role: RoleSystem},                       // dropped: no content
		{Role: RoleSystem, Content: "sys"},       // a user text block
		{Role: RoleUser},                         // dropped: nothing to send
		{Role: RoleAssistant},                    // dropped: no blocks
		{Role: RoleAssistant, Reasoning: "why"},  // a thinking block alone
		{Role: RoleAssistant, Content: "answer"}, // a text block alone
		{Role: RoleUser, Content: "go"},
		// The previous message is a user one but not a tool result, so this
		// starts a message of its own, and the next one joins it.
		{Role: "tool", Content: "r1", ToolCallID: "t1"},
		{Role: "tool", Content: "r2", ToolCallID: "t2"},
	})
	if len(msgs) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(msgs), msgs)
	}
	if msgs[0].Role != RoleUser || msgs[0].Content[0].Text != "sys" {
		t.Errorf("system message = %+v", msgs[0])
	}
	if msgs[1].Role != RoleAssistant || len(msgs[1].Content) != 1 ||
		msgs[1].Content[0].Type != "thinking" || msgs[1].Content[0].Thinking != "why" {
		t.Errorf("thinking message = %+v", msgs[1])
	}
	if msgs[2].Role != RoleAssistant || len(msgs[2].Content) != 1 ||
		msgs[2].Content[0].Type != "text" || msgs[2].Content[0].Text != "answer" {
		t.Errorf("text message = %+v", msgs[2])
	}
	if msgs[3].Role != RoleUser || msgs[3].Content[0].Text != "go" {
		t.Errorf("user message = %+v", msgs[3])
	}
	if len(msgs[4].Content) != 2 {
		t.Fatalf("tool results = %+v, want two blocks", msgs[4])
	}
	for i, want := range []string{"t1", "t2"} {
		if msgs[4].Content[i].ToolUseID != want {
			t.Errorf("tool_result %d = %+v", i, msgs[4].Content[i])
		}
	}

	// A message with no blocks at all is not a tool result message, and it is
	// what keeps the grouping above from appending into the wrong message.
	if isToolResultMessage(anthropicMessage{}) {
		t.Error("an empty message reported as a tool result message")
	}
}

// TestAnthropicStreamEdgeCases feeds the shapes a real stream may carry around
// the useful events: a comment line, an empty payload, an event that is not
// JSON, and the deltas the provider has to tolerate without a block of its own.
func TestAnthropicStreamEdgeCases(t *testing.T) {
	srv, _ := anthropicTestServer(t, []string{
		`event: ping`,
		`data: `,
		`data: [DONE]`,
		`data: not-json`,
		`data: {"type":"message_start","message":null}`,
		`data: {"type":"content_block_start","index":0}`,
		`data: {"type":"content_block_delta","index":0}`,
		`data: {"type":"content_block_delta","index":5,"delta":{"type":"text_delta","text":"hi"}}`,
		`data: {"type":"content_block_delta","index":5,"delta":{"type":"thinking_delta","thinking":""}}`,
		`data: {"type":"content_block_delta","index":6,"delta":{"type":"signature_delta","signature":"sg"}}`,
		`data: {"type":"message_delta"}`,
		`data: {"type":"message_delta","usage":{"input_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":5,"output_tokens":7}}`,
		`data: {"type":"message_stop"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")

	// A nil onChunk is allowed: the content still accumulates.
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hi" {
		t.Errorf("Content = %q, want hi", resp.Content)
	}
	if resp.Reasoning != "" || resp.Signature != "" {
		t.Errorf("Reasoning = %q Signature = %q, want both empty", resp.Reasoning, resp.Signature)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("StopReason = %q, want end_turn without a stop_reason", resp.StopReason)
	}
	if resp.Model != "claude-sonnet-5-5" {
		t.Errorf("Model = %q, want the requested model", resp.Model)
	}
	// No message_start usage: the merge starts from zero, and every counter of
	// the message_delta is taken as it came. The input and both cache counters
	// were all billed as prompt tokens.
	if resp.Usage == nil || resp.Usage.CompletionTokens != 7 ||
		resp.Usage.PromptTokens != 35 || resp.Usage.CachedTokens != 20 {
		t.Errorf("Usage = %+v, want completion=7 prompt=35 cached=20", resp.Usage)
	}
}

// TestAnthropicErrorWithoutBody checks the error event with no error object is
// still an error, not a silent end of stream.
func TestAnthropicErrorWithoutBody(t *testing.T) {
	srv, _ := anthropicTestServer(t, []string{
		`data: {"type":"error"}`,
	})
	p := NewAnthropicProvider("key", srv.URL, "claude-sonnet-5-5")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "anthropic api error") {
		t.Fatalf("expected an api error, got %v", err)
	}
}
