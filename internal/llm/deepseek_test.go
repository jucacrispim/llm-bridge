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
	if p.model != "deepseek-chat" {
		t.Errorf("model = %q, want deepseek-chat", p.model)
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
