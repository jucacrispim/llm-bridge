package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"llm-bridge/llm"
)

type fakeProvider struct {
	name      string
	model     string
	responses []*llm.ChatResponse
	resp      *llm.ChatResponse
	err       error
	skipChunk bool
	callCount int
}

func (f *fakeProvider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(string)) (*llm.ChatResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	var r *llm.ChatResponse
	if f.responses != nil {
		if f.callCount < len(f.responses) {
			r = f.responses[f.callCount]
			f.callCount++
		} else {
			r = f.responses[len(f.responses)-1]
		}
	} else {
		r = f.resp
	}
	if r == nil {
		return nil, nil
	}
	if onChunk != nil && !f.skipChunk && r.Content != "" {
		onChunk(r.Content)
	}
	return r, nil
}

func (f *fakeProvider) Name() string { return f.name }

func (f *fakeProvider) Model() string {
	if f.model != "" {
		return f.model
	}
	return "fake"
}

func newTestState() *state {
	return &state{
		provider: &fakeProvider{
			name: "fake",
			resp: &llm.ChatResponse{
				Content:    "Hello",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
			},
		},
	}
}

func TestHandleLineEmpty(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, _ := handleLine("", st, &w)
	if resp != "" {
		t.Errorf("empty line should produce no response, got %q", resp)
	}
}

func TestHandleLineInvalidJSON(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, _ := handleLine(`{"method":`, st, &w)
	expected := `{"event":"error","message":"invalid JSON: unexpected end of JSON input"}`
	if resp != expected {
		t.Errorf("invalid JSON response mismatch:\n got  %s\n want %s", resp, expected)
	}
}

func TestHandleLinePromptSuccess(t *testing.T) {
	st := newTestState()
	st.history = nil
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}

	want := `{"event":"chunk","text":"Hello"}` + "\n" + `{"event":"turn_end","stop_reason":"END_TURN","context_pct":0,"model":"fake","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n" + `{"event":"usage_delta","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n"
	if w.String() != want {
		t.Errorf("prompt output mismatch:\n got  %q\n want %q", w.String(), want)
	}
	if len(st.history) == 0 {
		t.Fatal("expected non-empty history after turn")
	}
}

func TestHandleLinePromptProviderError(t *testing.T) {
	st := &state{provider: &fakeProvider{err: errors.New("boom")}}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	resp, _ := handleLine(line, st, &w)
	want := `{"event":"error","message":"boom"}`
	if resp != want {
		t.Errorf("provider error response mismatch:\n got  %s\n want %s", resp, want)
	}
}

func TestHandleLinePromptNoProvider(t *testing.T) {
	st := &state{}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	resp, _ := handleLine(line, st, &w)
	want := `{"event":"error","message":"no LLM provider configured"}`
	if resp != want {
		t.Errorf("no provider response mismatch:\n got  %s\n want %s", resp, want)
	}
}

func TestHandleLinePromptNoChunk(t *testing.T) {
	st := &state{
		provider: &fakeProvider{
			name: "fake",
			resp: &llm.ChatResponse{
				Content:    "Hello",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
			},
			skipChunk: true,
		},
	}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}

	want := `{"event":"chunk","text":"Hello"}` + "\n" + `{"event":"turn_end","stop_reason":"END_TURN","context_pct":0,"model":"fake","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n" + `{"event":"usage_delta","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n"
	if w.String() != want {
		t.Errorf("prompt output mismatch:\n got  %q\n want %q", w.String(), want)
	}
}

func TestHandleLinePromptToolCall(t *testing.T) {
	st := &state{provider: &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{
			Content:    "",
			StopReason: "tool_calls",
			Usage:      &llm.Usage{},
			ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`},
			},
		},
	}}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	resp, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty returned response, got %q", resp)
	}
	got := w.String()
	if !strings.Contains(got, `"event":"tool_call"`) ||
		!strings.Contains(got, `"id":"call_1"`) ||
		!strings.Contains(got, `"name":"read"`) ||
		!strings.Contains(got, `"path":"/tmp/x"`) {
		t.Fatalf("expected tool_call event in output, got %q", got)
	}
}

func TestHandleLinePromptToolCallNoArgs(t *testing.T) {
	st := &state{provider: &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{
			Content:    "",
			StopReason: "tool_calls",
			Usage:      &llm.Usage{},
			ToolCalls: []llm.ToolCall{
				{ID: "call_2", Name: "glob", Arguments: ""},
			},
		},
	}}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	resp, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty returned response, got %q", resp)
	}
	got := w.String()
	if !strings.Contains(got, `"event":"tool_call"`) ||
		!strings.Contains(got, `"id":"call_2"`) ||
		!strings.Contains(got, `"name":"glob"`) ||
		!strings.Contains(got, `"input":{}`) {
		t.Fatalf("expected tool_call event with empty input in output, got %q", got)
	}
}

func TestHandleLinePromptBadParams(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	line := `{"method":"prompt","params":"bad"}`
	resp, _ := handleLine(line, st, &w)
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "invalid prompt params") {
		t.Errorf("expected error for invalid prompt params, got %q", resp)
	}
}

func TestHandleLineCancel(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Errorf("cancel response mismatch: %q", resp)
	}
}

func TestHandleLineCancelResetsHistory(t *testing.T) {
	st := newTestState()
	st.history = []llm.Message{
		{Role: llm.RoleUser, Content: "primeira"},
		{Role: llm.RoleUser, Content: "segunda"},
	}
	st.historyLenBeforeTurn = 1
	var w bytes.Buffer
	resp, _ := handleLine(`{"method":"cancel"}`, st, &w)
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}
	if len(st.history) != 1 || st.history[0].Content != "primeira" {
		t.Fatalf("history not reset correctly: %+v", st.history)
	}
	if st.inToolCycle {
		t.Fatal("inToolCycle should be false after cancel")
	}
}


func TestHandleLineSetCwd(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	line := `{"method":"set_cwd","params":{"cwd":"/tmp/proj"}}`
	resp, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("set_cwd should not quit")
	}
	if resp != "" {
		t.Errorf("set_cwd should have no response, got %q", resp)
	}
	if st.cwd != "/tmp/proj" {
		t.Errorf("cwd = %q, want /tmp/proj", st.cwd)
	}
}

func TestHandleLineSetCwdInvalid(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, _ := handleLine(`{"method":"set_cwd","params":"bad"}`, st, &w)
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "invalid set_cwd params") {
		t.Errorf("expected error for invalid set_cwd, got %q", resp)
	}
}

func TestHandleLineSetKnowledgeBases(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	line := `{"method":"set_knowledge_bases","params":{"bases":[]}}`
	resp, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("set_knowledge_bases should not quit")
	}
	if resp != "" {
		t.Errorf("set_knowledge_bases should have no response, got %q", resp)
	}
	if len(st.knowledgeBases) != 0 {
		t.Errorf("knowledgeBases len = %d, want 0", len(st.knowledgeBases))
	}
}

func TestHandleLineSetKnowledgeBasesInvalid(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, _ := handleLine(`{"method":"set_knowledge_bases","params":123}`, st, &w)
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "invalid set_knowledge_bases params") {
		t.Errorf("expected error for invalid set_knowledge_bases, got %q", resp)
	}
}

func TestHandleLineToolResult(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"tool_result","params":{"id":"a"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if resp != `{"event":"error","message":"tool_result without pending tool call"}` {
		t.Errorf("tool_result response mismatch: %q", resp)
	}
}

func TestHandleLineQuit(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"quit"}`, st, &w)
	if !quit {
		t.Fatal("quit should set quit flag")
	}
	if resp != "" {
		t.Errorf("quit should have no response, got %q", resp)
	}
}

func TestHandleLineUnknown(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"foo"}`, st, &w)
	if quit {
		t.Fatal("unknown method should not quit")
	}
	if resp != `{"event":"error","message":"unknown method foo"}` {
		t.Errorf("unknown method response mismatch: %q", resp)
	}
}

func TestRunWithProviderBasic(t *testing.T) {
	input := `{"method":"set_cwd","params":{"cwd":"/home/user"}}
{"method":"prompt","params":{"text":"oi"}}
{"method":"quit"}
`
	var out bytes.Buffer
	provider := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hi", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	err := runWithProvider(strings.NewReader(input), &out, provider)
	if err != nil {
		t.Fatalf("runWithProvider error: %v", err)
	}

	got := out.String()
	expected := `{"event":"ready"}` + "\n" +
		`{"event":"chunk","text":"Hi"}` + "\n" +
		`{"event":"turn_end","stop_reason":"END_TURN","context_pct":0,"model":"fake","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n" +
		`{"event":"usage_delta","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n"
	if got != expected {
		t.Errorf("runWithProvider output:\n got: %q\nwant: %q", got, expected)
	}
}

func TestHandleLinePromptToolCallThenResult(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
			{
				Content:    "done",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
			},
		},
	}
	st := &state{provider: provider}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"oi"}}`
	resp, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty response after prompt, got %q", resp)
	}
	got := w.String()
	if !strings.Contains(got, `"event":"tool_call"`) || !strings.Contains(got, `"id":"call_1"`) {
		t.Fatalf("expected tool_call event in initial output, got %q", got)
	}
	if strings.Contains(got, `"event":"turn_end"`) {
		t.Fatalf("should not emit turn_end before tool_result, got %q", got)
	}
	// now send tool_result
	w.Reset()
	resp, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"file content"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty response after tool_result, got %q", resp)
	}
	got = w.String()
	if !strings.Contains(got, `"event":"chunk"`) || !strings.Contains(got, `"text":"done"`) {
		t.Fatalf("expected chunk 'done' after tool_result, got %q", got)
	}
	if !strings.Contains(got, `"event":"turn_end"`) {
		t.Fatalf("expected turn_end after tool_result, got %q", got)
	}
}

func TestRunWithProviderEOF(t *testing.T) {
	var out bytes.Buffer
	provider := &fakeProvider{}
	err := runWithProvider(strings.NewReader(""), &out, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := out.String()
	want := `{"event":"ready"}` + "\n"
	if got != want {
		t.Errorf("runWithProvider EOF output:\n got: %q\nwant: %q", got, want)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("boom")
}

func TestRunWithProviderScannerError(t *testing.T) {
	var out bytes.Buffer
	provider := &fakeProvider{}
	err := runWithProvider(errorReader{}, &out, provider)
	if err == nil {
		t.Fatal("expected error from scanner")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error should contain boom, got %v", err)
	}
}

func TestHandleLinePromptInToolCycle(t *testing.T) {
	st := newTestState()
	st.inToolCycle = true
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"prompt","params":{"text":"x"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "cannot send new prompt while awaiting tool results") {
		t.Fatalf("expected error about pending tool, got %q", resp)
	}
}

func TestHandleLineToolResultInvalidParams(t *testing.T) {
	st := &state{
		inToolCycle:        true,
		pendingToolIDs:     []string{"call_1"},
		pendingToolResults: map[string]json.RawMessage{},
	}
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"tool_result","params":"bad"}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "invalid tool_result params") {
		t.Fatalf("expected invalid params error, got %q", resp)
	}
}

func TestHandleLineToolResultDuplicate(t *testing.T) {
	st := &state{
		inToolCycle:        true,
		pendingToolIDs:     []string{"call_1"},
		pendingToolResults: map[string]json.RawMessage{"call_1": json.RawMessage(`"existing"`)},
	}
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"new"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "duplicate tool_result for id call_1") {
		t.Fatalf("expected duplicate error, got %q", resp)
	}
}

func TestHandleLineToolResultPartialWait(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read", Arguments: `{}`},
					{ID: "call_2", Name: "glob", Arguments: `{}`},
				},
			},
		},
	}
	st := &state{provider: provider}
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"prompt","params":{"text":"oi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty response after prompt, got %q", resp)
	}
	if provider.callCount != 1 {
		t.Fatalf("expected provider called once, got %d", provider.callCount)
	}
	w.Reset()
	resp, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"ok"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty response after partial tool_result, got %q", resp)
	}
	if w.Len() != 0 {
		t.Fatalf("expected no output after partial tool_result, got %q", w.String())
	}
	if provider.callCount != 1 {
		t.Fatalf("expected provider NOT called again, got %d", provider.callCount)
	}
}

func TestHandleLineCancelWithStoredCancelFunc(t *testing.T) {
	st := newTestState()
	canceled := false
	st.cancel = func() { canceled = true }
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}
	if !canceled {
		t.Fatal("expected stored cancel func to be invoked")
	}
}

func TestHandleLinePromptContextCanceled(t *testing.T) {
	st := &state{provider: &fakeProvider{err: context.Canceled}}
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"prompt","params":{"text":"oi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if resp != "" {
		t.Fatalf("expected empty response after context canceled, got %q", resp)
	}
	if w.Len() != 0 {
		t.Fatalf("expected no output after context canceled, got %q", w.String())
	}
}
