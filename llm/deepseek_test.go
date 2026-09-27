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
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("network down")
}

func TestNewDeepSeekProvider(t *testing.T) {
	p := NewDeepSeekProvider("test-key", "https://api.deepseek.com/v1/chat/completions", "deepseek-chat")
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.apiKey != "test-key" || p.endpoint != "https://api.deepseek.com/v1/chat/completions" || p.model != "deepseek-chat" {
		t.Errorf("unexpected provider fields: %+v", p)
	}
}

func TestNewDeepSeekProviderFromEnvCustom(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "custom-key")
	t.Setenv("DEEPSEEK_URL", "https://example.com/chat")
	t.Setenv("DEEPSEEK_MODEL", "deepseek-coder")

	p := NewDeepSeekProviderFromEnv()
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.apiKey != "custom-key" {
		t.Errorf("apiKey = %q, want custom-key", p.apiKey)
	}
	if p.endpoint != "https://example.com/chat" {
		t.Errorf("endpoint = %q, want custom url", p.endpoint)
	}
	if p.model != "deepseek-coder" {
		t.Errorf("model = %q, want deepseek-coder", p.model)
	}
}

func TestNewDeepSeekProviderFromEnvDefaults(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "")
	t.Setenv("DEEPSEEK_URL", "")
	t.Setenv("DEEPSEEK_MODEL", "")
	t.Setenv("DEEPSEEK_THINKING", "")

	p := NewDeepSeekProviderFromEnv()
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.apiKey != "" {
		t.Errorf("apiKey = %q, want empty", p.apiKey)
	}
	if p.endpoint != "https://api.deepseek.com/chat/completions" {
		t.Errorf("endpoint = %q, want default", p.endpoint)
	}
	// thinking is on by default, so the derived model is deepseek-flash
	if p.Model() != "deepseek-flash" {
		t.Errorf("Model() = %q, want deepseek-flash (thinking default)", p.Model())
	}
	if p.explicitModel {
		t.Errorf("explicitModel = true, want false (no DEEPSEEK_MODEL)")
	}
	if !p.Thinking() {
		t.Errorf("Thinking() = false, want true by default")
	}
	if p.ReasoningEffort() != DefaultReasoningEffort {
		t.Errorf("ReasoningEffort() = %q, want %q", p.ReasoningEffort(), DefaultReasoningEffort)
	}
}

func TestNewDeepSeekProviderFromEnvThinkingDisabled(t *testing.T) {
	t.Setenv("DEEPSEEK_API_KEY", "key")
	t.Setenv("DEEPSEEK_URL", "")
	t.Setenv("DEEPSEEK_MODEL", "")
	t.Setenv("DEEPSEEK_THINKING", "false")

	p := NewDeepSeekProviderFromEnv()
	if p == nil {
		t.Fatal("nil provider")
	}
	if p.Thinking() {
		t.Errorf("Thinking() = true, want false")
	}
	if p.Model() != "deepseek-chat" {
		t.Errorf("Model() = %q, want deepseek-chat when thinking is off", p.Model())
	}
}

func TestDefaultModel(t *testing.T) {
	if got := DefaultModel(true); got != "deepseek-flash" {
		t.Errorf("DefaultModel(true) = %q, want deepseek-flash", got)
	}
	if got := DefaultModel(false); got != "deepseek-chat" {
		t.Errorf("DefaultModel(false) = %q, want deepseek-chat", got)
	}
}

func TestThinkingFromEnv(t *testing.T) {
	for _, tc := range []struct {
		val string
		def bool
		exp bool
	}{
		{"", true, true},
		{"", false, false},
		{"true", true, true},
		{"1", true, true},
		{"yes", true, true},
		{"on", true, true},
		{"false", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		{"garbage", true, true},
	} {
		t.Setenv("DEEPSEEK_THINKING", tc.val)
		if got := ThinkingFromEnv(tc.def); got != tc.exp {
			t.Errorf("ThinkingFromEnv(%q, %v) = %v, want %v", tc.val, tc.def, got, tc.exp)
		}
	}
}

func TestThinkingGetterSetter(t *testing.T) {
	p := NewDeepSeekProvider("key", "https://example.com", "deepseek-chat")
	if !p.Thinking() {
		t.Fatal("Thinking() = false, want true by default")
	}
	p.SetThinking(false)
	if p.Thinking() {
		t.Fatal("Thinking() = true after SetThinking(false)")
	}
	p.SetThinking(true)
	if !p.Thinking() {
		t.Fatal("Thinking() = false after SetThinking(true)")
	}
}

func TestNewDeepSeekProviderWithThinking(t *testing.T) {
	p := NewDeepSeekProviderWithThinking("key", "https://example.com", "deepseek-chat", false)
	if p.Thinking() {
		t.Fatal("Thinking() = true, want false")
	}
	if p.model != "deepseek-chat" {
		t.Errorf("model = %q, want deepseek-chat", p.model)
	}
	if !p.explicitModel {
		t.Error("explicitModel = false, want true (model was passed explicitly)")
	}
	if p.ReasoningEffort() != DefaultReasoningEffort {
		t.Errorf("ReasoningEffort() = %q, want %q", p.ReasoningEffort(), DefaultReasoningEffort)
	}
}

func TestNewDeepSeekProviderAutoModel(t *testing.T) {
	p := NewDeepSeekProviderAutoModel("key", "https://example.com", true)
	if p.explicitModel {
		t.Error("explicitModel = true, want false")
	}
	if p.Model() != "deepseek-flash" {
		t.Errorf("Model() = %q, want deepseek-flash", p.Model())
	}
	p.SetThinking(false)
	if p.Model() != "deepseek-chat" {
		t.Errorf("Model() after SetThinking(false) = %q, want deepseek-chat", p.Model())
	}
}

func TestChatSendsModelAndResolveModel(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// An explicitly configured model ALWAYS wins, even when a request overrides
	// the thinking mode (the "gotcha" fix): deepseek-chat stays deepseek-chat.
	p := NewDeepSeekProviderWithThinking("key", server.URL, "deepseek-chat", false)
	thinking := true
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Thinking: &thinking,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-chat"`) {
		t.Fatalf("expected explicit deepseek-chat in request body, got %s", gotBody)
	}
	if resp.Model != "deepseek-chat" {
		t.Errorf("resp.Model = %q, want deepseek-chat (explicit model wins)", resp.Model)
	}

	// explicit req.Model wins over everything
	gotBody = ""
	_, err = p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Model:    "my-custom-model",
		Thinking: &thinking,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"my-custom-model"`) {
		t.Fatalf("expected my-custom-model in request body, got %s", gotBody)
	}
}

func TestChatSendsStreamOptionsAndParsesCacheUsage(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n" +
				"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":3,\"total_tokens\":103,\"prompt_cache_hit_tokens\":80,\"prompt_cache_miss_tokens\":20}}\n\n" +
				"data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProviderWithThinking("key", server.URL, "deepseek-chat", false)
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// usage only arrives if the stream asks for it explicitly.
	if !strings.Contains(gotBody, `"stream_options":{"include_usage":true}`) {
		t.Fatalf("expected stream_options include_usage in request body, got %s", gotBody)
	}
	if resp.Usage == nil || resp.Usage.CacheHitTokens != 80 || resp.Usage.CacheMissTokens != 20 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestChatAutoModelSwitchesWithThinking(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// auto-model provider with thinking OFF; a per-request override to ON must
	// switch the derived model to deepseek-flash.
	p := NewDeepSeekProviderAutoModel("key", server.URL, false)
	thinking := true
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Thinking: &thinking,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-flash"`) {
		t.Fatalf("expected deepseek-flash in request body, got %s", gotBody)
	}
	if resp.Model != "deepseek-flash" {
		t.Errorf("resp.Model = %q, want deepseek-flash", resp.Model)
	}

	// no override: model derives from provider thinking (off → deepseek-chat)
	gotBody = ""
	_, err = p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-chat"`) {
		t.Fatalf("expected deepseek-chat in request body, got %s", gotBody)
	}
}

func TestChatSendsReasoningEffortWhenThinking(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// explicit model with thinking ON: reasoning_effort=high must be sent
	p := NewDeepSeekProvider("key", server.URL, "deepseek-v4-flash")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"reasoning_effort":"high"`) {
		t.Fatalf("expected reasoning_effort=high in request body, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-v4-flash"`) {
		t.Fatalf("expected deepseek-v4-flash in request body, got %s", gotBody)
	}
}

func TestChatOmitsReasoningEffortWhenThinkingOff(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProviderWithThinking("key", server.URL, "deepseek-v4-flash", false)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "reasoning_effort") {
		t.Fatalf("expected NO reasoning_effort in request body when thinking is off, got %s", gotBody)
	}
	// v4 models reason by default; with thinking off the structured
	// `thinking: {"type":"disabled"}` must be sent so they stop reasoning.
	if !strings.Contains(gotBody, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("expected thinking disabled in request body when thinking is off, got %s", gotBody)
	}
}

func TestChatSendsThinkingEnabledWhenThinkingOn(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-v4-flash")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// thinking on: explicit enabled struct + reasoning_effort (default high)
	if !strings.Contains(gotBody, `"thinking":{"type":"enabled"}`) {
		t.Fatalf("expected thinking enabled in request body, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"reasoning_effort":"high"`) {
		t.Fatalf("expected reasoning_effort=high in request body, got %s", gotBody)
	}
}

func TestChatSendsThinkingDisabledPerRequestOverride(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// provider default is thinking ON, but a per-request override to off must
	// win and send the disabled struct (and drop reasoning_effort).
	p := NewDeepSeekProvider("key", server.URL, "deepseek-v4-flash")
	thinking := false
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Thinking: &thinking,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"thinking":{"type":"disabled"}`) {
		t.Fatalf("expected thinking disabled (per-request) in request body, got %s", gotBody)
	}
	if strings.Contains(gotBody, "reasoning_effort") {
		t.Fatalf("expected NO reasoning_effort when thinking off (per-request), got %s", gotBody)
	}
}

func TestSetReasoningEffort(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	if p.ReasoningEffort() != DefaultReasoningEffort {
		t.Fatalf("ReasoningEffort() = %q, want default %q", p.ReasoningEffort(), DefaultReasoningEffort)
	}
	p.SetReasoningEffort("low")
	if p.ReasoningEffort() != "low" {
		t.Fatalf("ReasoningEffort() = %q, want low", p.ReasoningEffort())
	}
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"reasoning_effort":"low"`) {
		t.Fatalf("expected reasoning_effort=low in request body, got %s", gotBody)
	}
	// empty disables sending the parameter
	p.SetReasoningEffort("")
	gotBody = ""
	_, err = p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "reasoning_effort") {
		t.Fatalf("expected NO reasoning_effort after SetReasoningEffort(\"\"), got %s", gotBody)
	}
}

func TestChatReasoningEffortPerRequestWins(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// provider global is "high"; a per-request "low" must win
	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	if p.ReasoningEffort() != DefaultReasoningEffort {
		t.Fatalf("ReasoningEffort() = %q, want default %q", p.ReasoningEffort(), DefaultReasoningEffort)
	}
	effort := "low"
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages:        []Message{{Role: RoleUser, Content: "hello"}},
		ReasoningEffort: &effort,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"reasoning_effort":"low"`) {
		t.Fatalf("expected per-request reasoning_effort=low in request body, got %s", gotBody)
	}
	if strings.Contains(gotBody, `"reasoning_effort":"high"`) {
		t.Fatalf("provider global reasoning_effort=high leaked through, got %s", gotBody)
	}
}

func TestChatReasoningEffortPerRequestEmptyDisables(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	// provider global is "high"; a per-request empty string must disable it
	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	effort := ""
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages:        []Message{{Role: RoleUser, Content: "hello"}},
		ReasoningEffort: &effort,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "reasoning_effort") {
		t.Fatalf("expected NO reasoning_effort with per-request empty override, got %s", gotBody)
	}
}

func TestChatReasoningEffortPerRequestIgnoredWhenThinkingOff(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProviderWithThinking("key", server.URL, "deepseek-chat", false)
	effort := "low"
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages:        []Message{{Role: RoleUser, Content: "hello"}},
		ReasoningEffort: &effort,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "reasoning_effort") {
		t.Fatalf("expected NO reasoning_effort when thinking is off, got %s", gotBody)
	}
}

func TestChatResolveModelNoOverride(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProviderWithThinking("key", server.URL, "deepseek-reasoner", true)
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"model":"deepseek-reasoner"`) {
		t.Fatalf("expected deepseek-reasoner in request body, got %s", gotBody)
	}
}

func TestProviderName(t *testing.T) {
	p := NewDeepSeekProvider("test-key", "https://api.deepseek.com/v1/chat/completions", "deepseek-chat")
	if name := p.Name(); name != "deepseek" {
		t.Errorf("Name() = %q, want deepseek", name)
	}
}

func TestName(t *testing.T) {
	p := &DeepSeekProvider{}
	if p.Name() != "deepseek" {
		t.Fatalf("expected deepseek, got %q", p.Name())
	}
}

func TestChatStreaming(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := "" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\" world\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":3,\"total_tokens\":5}}\n\n" +
			"data: [DONE]\n\n"
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	var chunks []string
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(s string) { chunks = append(chunks, s) })
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "Hello world" {
		t.Fatalf("expected 'Hello world', got %q", resp.Content)
	}
	if resp.StopReason != "stop" {
		t.Fatalf("expected stop, got %q", resp.StopReason)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 5 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if len(chunks) != 2 || chunks[0] != "Hello" || chunks[1] != " world" {
		t.Fatalf("unexpected chunks: %#v", chunks)
	}
}

func TestChatStreamingToolCall(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := "" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"read\",\"arguments\":\"\"}}]}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"path\\\":\\\"/tmp/x\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n" +
			"data: [DONE]\n\n"
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "read" || tc.Arguments != `{"path":"/tmp/x"}` {
		t.Fatalf("unexpected tool call: %+v", tc)
	}
}

func TestChatIgnoresNonDataLines(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := "" +
			"ping\n\n" +
			"data: not-json\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
			"data: [DONE]\n\n"
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "ok" {
		t.Fatalf("expected content ok, got %q", resp.Content)
	}
}

func TestChatNoChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(string) {})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Content != "" {
		t.Fatalf("expected empty content, got %q", resp.Content)
	}
	if resp.StopReason != "END_TURN" {
		t.Fatalf("expected END_TURN, got %q", resp.StopReason)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
}

func TestChatServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(string) {})
	if err == nil {
		t.Fatal("expected error from server")
	}
	if !strings.Contains(err.Error(), "HTTP 400") {
		t.Fatalf("expected HTTP 400, got %v", err)
	}
}

func TestChatNetworkError(t *testing.T) {
	p := NewDeepSeekProvider("key", "http://unused.invalid", "model")
	p.client = &http.Client{Transport: errTransport{}}
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, func(string) {})
	if err == nil {
		t.Fatal("expected network error")
	}
}

func TestChatScannerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		big := "data: " + strings.Repeat("x", 200*1024) + "\n\n"
		_, _ = w.Write([]byte(big))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: "user", Content: "hello"}},
	}, nil)
	if err == nil {
		t.Fatal("expected scanner error")
	}
}

func TestChatSendsSystemPrompt(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-chat")
	_, err := p.Chat(context.Background(), ChatRequest{
		System:   "You are a coding assistant. Do not call tools for greetings.",
		Messages: []Message{{Role: RoleUser, Content: "oi"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `{"role":"system","content":"You are a coding assistant. Do not call tools for greetings."}`) {
		t.Fatalf("expected system message in request body, got %s", gotBody)
	}
}

func TestChatWithTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hello"}},
		Tools: []Tool{
			{Name: "read", Description: "Read a file", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestModel(t *testing.T) {
	p := NewDeepSeekProvider("key", "https://example.com", "deepseek-model")
	if got := p.Model(); got != "deepseek-model" {
		t.Errorf("Model() = %q, want deepseek-model", got)
	}
}

func TestChatSendsImageContentParts(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-v4-flash")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "what is this?", Images: []Image{
				{MIME: "image/png", Data: "AAAA", Detail: "high"},
				{URL: "https://example.com/x.jpg"},
			}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A message with images serializes content as the multimodal parts array:
	// text first, then one image_url per image (inline data URL / pass-through).
	want := `"content":[{"type":"text","text":"what is this?"},` +
		`{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA","detail":"high"}},` +
		`{"type":"image_url","image_url":{"url":"https://example.com/x.jpg"}}]`
	if !strings.Contains(gotBody, want) {
		t.Fatalf("expected multimodal content in request body, got %s", gotBody)
	}
}

func TestChatIgnoresImagesOnNonUserRole(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-v4-flash")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleAssistant, Content: "hi", Images: []Image{{MIME: "image/png", Data: "AAAA"}}},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Images are only valid on user messages, so an image on an assistant
	// message is dropped and the content stays a plain string.
	if strings.Contains(gotBody, "image_url") {
		t.Fatalf("images on non-user role should be ignored, got %s", gotBody)
	}
	if !strings.Contains(gotBody, `"content":"hi"`) {
		t.Fatalf("expected plain string content, got %s", gotBody)
	}
}

func TestChatSendsToolCallsInMessage(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "model")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{
				Role:      RoleAssistant,
				Content:   "",
				ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"tool_calls":`) || !strings.Contains(gotBody, `"id":"call_1"`) {
		t.Fatalf("expected tool_calls in request body, got %s", gotBody)
	}
}

func TestChatSendsReasoningBackForToolCallAssistant(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	// History: an assistant turn that performed a tool call carries its
	// reasoning_content; DeepSeek's thinking mode requires it to be sent back.
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "read the file"},
			{
				Role:      RoleAssistant,
				Content:   "let me read it",
				Reasoning: "I need the file contents first",
				ToolCalls: []ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
			{Role: "tool", ToolCallID: "call_1", Content: "file contents"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotBody, `"reasoning_content":"I need the file contents first"`) {
		t.Fatalf("expected reasoning_content sent back in request body, got %s", gotBody)
	}
}

func TestChatOmitsReasoningForFinalAssistant(t *testing.T) {
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	// A final answer without tool calls: reasoning is ignored by the API, so we
	// omit it to save tokens.
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "hello", Reasoning: "be polite"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(gotBody, "reasoning_content") {
		t.Fatalf("expected NO reasoning_content for final assistant message, got %s", gotBody)
	}
}

func TestChatStreamingCapturesReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		body := "" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step one \"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step two\"}}]}\n\n" +
			"data: {\"choices\":[{\"delta\":{\"content\":\"Final answer\"},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n"
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	p := NewDeepSeekProvider("key", server.URL, "deepseek-reasoner")
	var reasoningChunks []string
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "hello"}},
		OnReasoning: func(s string) { reasoningChunks = append(reasoningChunks, s) },
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Reasoning != "step one step two" {
		t.Fatalf("Reasoning = %q, want %q", resp.Reasoning, "step one step two")
	}
	if resp.Content != "Final answer" {
		t.Fatalf("Content = %q, want %q", resp.Content, "Final answer")
	}
	if len(reasoningChunks) != 2 || reasoningChunks[0] != "step one " || reasoningChunks[1] != "step two" {
		t.Fatalf("unexpected reasoning chunks: %#v", reasoningChunks)
	}
}
