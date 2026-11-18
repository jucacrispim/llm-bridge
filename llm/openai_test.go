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

// levelPtr returns a pointer to a reasoning level for a request override.
func levelPtr(l ReasoningLevel) *ReasoningLevel { return &l }

// openaiTestServer serves the given SSE events and records the request body.
func openaiTestServer(t *testing.T, events []string) (*httptest.Server, *string) {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.Header().Set("Content-Type", "text/event-stream")
		for _, e := range events {
			_, _ = w.Write([]byte("data: " + e + "\n\n"))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &body
}

// openaiRequestBody decodes the recorded body into a map.
func openaiRequestBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("request body is not JSON: %v (%s)", err, body)
	}
	return payload
}

func TestNewOpenAIProviderFromEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("OPENAI_URL", "")
	t.Setenv("OPENAI_MODEL", "")
	t.Setenv("OPENAI_THINKING", "")
	t.Setenv("OPENAI_REASONING_EFFORT", "")
	t.Setenv("OPENAI_REASONING_SUMMARY", "")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "")

	p := NewOpenAIProviderFromEnv()
	if p.Name() != "openai" {
		t.Errorf("Name() = %q, want openai", p.Name())
	}
	if p.Model() != DefaultOpenAIModel {
		t.Errorf("Model() = %q, want %q", p.Model(), DefaultOpenAIModel)
	}
	if p.endpoint != "https://api.openai.com/v1/responses" {
		t.Errorf("endpoint = %q", p.endpoint)
	}
	if !p.Thinking() {
		t.Error("Thinking() = false, want true")
	}
	// The summary is off unless asked for: it can need organization
	// verification, and a request that asks for it then fails whole.
	if p.ReasoningSummary() != "" {
		t.Errorf("ReasoningSummary() = %q, want empty", p.ReasoningSummary())
	}
	if p.MaxOutputTokens() != 0 {
		t.Errorf("MaxOutputTokens() = %d, want 0", p.MaxOutputTokens())
	}
}

func TestNewOpenAIProviderFromEnvCustom(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "key")
	t.Setenv("OPENAI_URL", "https://example.com/responses")
	t.Setenv("OPENAI_MODEL", "gpt-6.2-astra")
	t.Setenv("OPENAI_THINKING", "false")
	t.Setenv("OPENAI_REASONING_EFFORT", "xhigh")
	t.Setenv("OPENAI_REASONING_SUMMARY", "auto")
	t.Setenv("OPENAI_MAX_OUTPUT_TOKENS", "4096")

	p := NewOpenAIProviderFromEnv()
	if p.endpoint != "https://example.com/responses" {
		t.Errorf("endpoint = %q", p.endpoint)
	}
	if p.Model() != "gpt-6.2-astra" {
		t.Errorf("Model() = %q", p.Model())
	}
	if p.Thinking() {
		t.Error("Thinking() = true, want false")
	}
	if p.ReasoningEffort() != "xhigh" {
		t.Errorf("ReasoningEffort() = %q", p.ReasoningEffort())
	}
	if p.ReasoningSummary() != "auto" {
		t.Errorf("ReasoningSummary() = %q", p.ReasoningSummary())
	}
	if p.MaxOutputTokens() != 4096 {
		t.Errorf("MaxOutputTokens() = %d", p.MaxOutputTokens())
	}
}

func TestOpenAIChatRequestPayload(t *testing.T) {
	srv, lastBody := openaiTestServer(t, []string{
		`{"type":"response.completed","response":{"status":"completed","model":"gpt-6.2-sol"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	p.SetMaxOutputTokens(2048)
	_, err := p.Chat(context.Background(), ChatRequest{
		System: "be brief",
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
			{Role: RoleAssistant, Content: "reading", ToolCalls: []ToolCall{
				{ID: "call_1", Name: "read", Arguments: `{"path":"a"}`},
			}, Reasoning: "thought", Signature: `{"type":"reasoning","encrypted_content":"abc"}`},
			{Role: "tool", ToolCallID: "call_1", Content: "file body"},
		},
		Tools: []Tool{{
			Name:        "read",
			Description: "read a file",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
		ReasoningLevel: levelPtr(ReasoningHigh),
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	payload := openaiRequestBody(t, *lastBody)
	if payload["model"] != "gpt-6.2-sol" {
		t.Errorf("model = %v", payload["model"])
	}
	if payload["instructions"] != "be brief" {
		t.Errorf("instructions = %v", payload["instructions"])
	}
	if payload["store"] != false {
		t.Errorf("store = %v, want false", payload["store"])
	}
	if payload["stream"] != true {
		t.Errorf("stream = %v, want true", payload["stream"])
	}
	if payload["max_output_tokens"] != float64(2048) {
		t.Errorf("max_output_tokens = %v", payload["max_output_tokens"])
	}

	include, _ := payload["include"].([]any)
	if len(include) != 1 || include[0] != "reasoning.encrypted_content" {
		t.Errorf("include = %v, want the encrypted reasoning include", payload["include"])
	}

	reasoning, _ := payload["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" {
		t.Errorf("reasoning.effort = %v, want high", reasoning["effort"])
	}

	tools, _ := payload["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", payload["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "read" {
		t.Errorf("tool = %v", tool)
	}
	if _, ok := tool["strict"]; ok {
		t.Error("strict is sent, and the schemas do not satisfy it")
	}

	// A tool call is an item of its own, the reasoning item comes first and the
	// tool result is a function_call_output item.
	items, _ := payload["input"].([]any)
	if len(items) != 5 {
		t.Fatalf("input has %d items, want 5: %v", len(items), items)
	}
	first, _ := items[0].(map[string]any)
	if first["role"] != "user" || first["content"] != "hi" {
		t.Errorf("items[0] = %v", first)
	}
	second, _ := items[1].(map[string]any)
	if second["type"] != "reasoning" || second["encrypted_content"] != "abc" {
		t.Errorf("items[1] = %v, want the reasoning item replayed", second)
	}
	third, _ := items[2].(map[string]any)
	if third["role"] != "assistant" {
		t.Errorf("items[2] = %v", third)
	}
	fourth, _ := items[3].(map[string]any)
	if fourth["type"] != "function_call" || fourth["call_id"] != "call_1" || fourth["name"] != "read" {
		t.Errorf("items[3] = %v, want the function_call", fourth)
	}
	fifth, _ := items[4].(map[string]any)
	if fifth["type"] != "function_call_output" || fifth["call_id"] != "call_1" || fifth["output"] != "file body" {
		t.Errorf("items[4] = %v, want the function_call_output", fifth)
	}
}

func TestOpenAIThinkingOffOmitsReasoning(t *testing.T) {
	srv, lastBody := openaiTestServer(t, []string{
		`{"type":"response.completed","response":{"status":"completed"}}`,
	})
	p := NewOpenAIProviderWithThinking("key", srv.URL, "gpt-6.2-sol", false)
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	payload := openaiRequestBody(t, *lastBody)
	if _, ok := payload["reasoning"]; ok {
		t.Errorf("reasoning is sent with thinking off: %v", payload["reasoning"])
	}
	if _, ok := payload["include"]; ok {
		t.Errorf("include is sent with thinking off: %v", payload["include"])
	}
}

func TestOpenAIStreamTextAndUsage(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.created","response":{"model":"gpt-6.2-sol","status":"in_progress"}}`,
		`{"type":"response.output_text.delta","delta":"hel"}`,
		`{"type":"response.output_text.delta","delta":"lo"}`,
		`{"type":"response.completed","response":{"status":"completed","model":"gpt-6.2-sol",` +
			`"usage":{"input_tokens":1000,"output_tokens":500,"total_tokens":1500,` +
			`"input_tokens_details":{"cached_tokens":400},"output_tokens_details":{"reasoning_tokens":200}}}}`,
	})
	useModels(t, `{"providers":{"openai":{"levels":["low","high"],"models":[
		{"id":"gpt-6.2-sol","window":1050000,
		 "price":{"input":2.0,"cached":0.10,"output":10.0}}]}}}`)

	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	var chunks []string
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, func(s string) { chunks = append(chunks, s) })
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Content != "hello" {
		t.Errorf("Content = %q, want hello", resp.Content)
	}
	if strings.Join(chunks, "") != "hello" {
		t.Errorf("chunks = %v", chunks)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("StopReason = %q, want end_turn", resp.StopReason)
	}
	if resp.Usage.PromptTokens != 1000 || resp.Usage.CompletionTokens != 500 ||
		resp.Usage.TotalTokens != 1500 || resp.Usage.CachedTokens != 400 {
		t.Errorf("Usage = %+v", resp.Usage)
	}
	// 600 uncached at 2.00, 400 cached at 0.10 and 500 out at 10.00, per
	// million tokens.
	want := (600*2.0 + 400*0.10 + 500*10.0) / 1e6
	if resp.Usage.CostUSD == nil || *resp.Usage.CostUSD != want {
		t.Errorf("CostUSD = %v, want %v", resp.Usage.CostUSD, want)
	}
}

func TestOpenAIStreamToolCall(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read"}}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{\"path\":"}`,
		`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"\"a\"}"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"a\"}"}}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "read a"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("got %d tool calls, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "read" || tc.Arguments != `{"path":"a"}` {
		t.Errorf("ToolCall = %+v", tc)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("StopReason = %q, want tool_use", resp.StopReason)
	}
}

func TestOpenAIStreamReasoningAndSignature(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.reasoning_summary_text.delta","delta":"thinking"}`,
		`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs_1","encrypted_content":"cipher"}}`,
		`{"type":"response.output_text.delta","delta":"done"}`,
		`{"type":"response.completed","response":{"status":"completed"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	var reasoning []string
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages:    []Message{{Role: RoleUser, Content: "hi"}},
		OnReasoning: func(s string) { reasoning = append(reasoning, s) },
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.Reasoning != "thinking" {
		t.Errorf("Reasoning = %q", resp.Reasoning)
	}
	if strings.Join(reasoning, "") != "thinking" {
		t.Errorf("reasoning chunks = %v", reasoning)
	}
	if resp.Content != "done" {
		t.Errorf("Content = %q", resp.Content)
	}
	// The signature is the whole reasoning item, so the next request can replay
	// it with the encrypted content.
	var item map[string]any
	if err := json.Unmarshal([]byte(resp.Signature), &item); err != nil {
		t.Fatalf("Signature is not JSON: %v (%q)", err, resp.Signature)
	}
	if item["type"] != "reasoning" || item["encrypted_content"] != "cipher" {
		t.Errorf("Signature item = %v", item)
	}
}

func TestOpenAIIncomplete(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.incomplete","response":{"status":"incomplete",` +
			`"incomplete_details":{"reason":"max_output_tokens"}}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	resp, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.StopReason != StopMaxTokens {
		t.Errorf("StopReason = %q, want max_tokens", resp.StopReason)
	}
}

func TestOpenAIErrorEvent(t *testing.T) {
	event := `{"type":"error","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"You have no credits remaining. Add credits to continue using the API at https://platform.openai.com/settings/organization/billing/.","param":null},"sequence_number":2}`
	srv, _ := openaiTestServer(t, []string{event})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("want an error for the error event")
	}
	if !strings.Contains(err.Error(), "credit_balance_exhausted") {
		t.Errorf("error = %v, want the code", err)
	}
	if !strings.Contains(err.Error(), "no credits remaining") {
		t.Errorf("error = %v, want the message", err)
	}
}

func TestOpenAIFailedResponse(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.failed","response":{"status":"failed","error":{"type":"insufficient_quota","code":"credit_balance_exhausted","message":"You have no credits remaining."}}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("want an error for the failed response")
	}
	if !strings.Contains(err.Error(), "credit_balance_exhausted") {
		t.Errorf("error = %v, want the code", err)
	}
}

func TestOpenAIErrorEventWithoutDetail(t *testing.T) {
	// Neither code nor message: the raw event is the only thing left to show.
	srv, _ := openaiTestServer(t, []string{
		`{"type":"error","error":{"type":"server_error"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("want an error for the error event")
	}
	if !strings.Contains(err.Error(), `"type":"server_error"`) {
		t.Errorf("error = %v, want the raw event", err)
	}
}

func TestOpenAIStreamWithoutCompletion(t *testing.T) {
	srv, _ := openaiTestServer(t, []string{
		`{"type":"response.created","response":{"status":"in_progress"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("want an error for a stream with no completion event")
	}
}

func TestOpenAIHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"no key"}}`))
	}))
	t.Cleanup(srv.Close)

	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	_, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v, want the 401", err)
	}
}

func TestOpenAIImageContent(t *testing.T) {
	srv, lastBody := openaiTestServer(t, []string{
		`{"type":"response.completed","response":{"status":"completed"}}`,
	})
	p := NewOpenAIProvider("key", srv.URL, "gpt-6.2-sol")
	if _, err := p.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "what is this", Images: []Image{
			{MIME: "image/png", Data: "AAAA"},
		}}},
	}, nil); err != nil {
		t.Fatalf("Chat: %v", err)
	}

	payload := openaiRequestBody(t, *lastBody)
	items, _ := payload["input"].([]any)
	if len(items) != 1 {
		t.Fatalf("input = %v", items)
	}
	msg, _ := items[0].(map[string]any)
	parts, _ := msg["content"].([]any)
	if len(parts) != 2 {
		t.Fatalf("content parts = %v", parts)
	}
	text, _ := parts[0].(map[string]any)
	if text["type"] != "input_text" || text["text"] != "what is this" {
		t.Errorf("parts[0] = %v", text)
	}
	img, _ := parts[1].(map[string]any)
	if img["type"] != "input_image" || img["image_url"] != "data:image/png;base64,AAAA" {
		t.Errorf("parts[1] = %v", img)
	}
}

func TestOpenAIEmptyArgumentsBecomeObject(t *testing.T) {
	if got := openaiArguments("  "); got != "{}" {
		t.Errorf("openaiArguments(blank) = %q, want {}", got)
	}
	if got := openaiArguments(`{"a":1}`); got != `{"a":1}` {
		t.Errorf("openaiArguments(json) = %q", got)
	}
}

func TestOpenAIUsageWithoutPrice(t *testing.T) {
	// gpt-6.1-sol is in the catalog without a price, so the cost is unknown
	// instead of zero.
	if _, ok := LookupPrice("gpt-6.1-sol"); ok {
		t.Fatal("gpt-6.1-sol has a price, want none")
	}
	u := &openaiRespUsage{InputTokens: 10, OutputTokens: 5}
	if got := openaiResponsesCost("gpt-6.1-sol", u); got != nil {
		t.Errorf("cost = %v, want nil", got)
	}
}
