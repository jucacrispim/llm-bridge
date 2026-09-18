package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"llm-bridge/knowledge"
	"llm-bridge/llm"
)

type fakeProvider struct {
	name      string
	model     string
	responses []*llm.ChatResponse
	resp      *llm.ChatResponse
	err       error
	skipChunk bool
	// skipReasoning makes Chat return resp.Reasoning without invoking the
	// OnReasoning callback, simulating providers that don't stream the
	// chain-of-thought. It exercises the server's non-streaming fallback.
	skipReasoning bool
	callCount     int
	lastReq       llm.ChatRequest
}

func (f *fakeProvider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(string)) (*llm.ChatResponse, error) {
	f.lastReq = req
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
	if req.OnReasoning != nil && !f.skipReasoning && r.Reasoning != "" {
		req.OnReasoning(r.Reasoning)
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
		firstTurn: true,
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
	resp, _ := handleLine(line, st, &w)
	want := `{"event":"error","message":"boom"}`
	if resp != want {
		t.Errorf("provider error response mismatch:\n got  %s\n want %s", resp, want)
	}
}

func TestHandleLinePromptNoProvider(t *testing.T) {
	st := &state{}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi"}}`
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}

	want := `{"event":"chunk","text":"Hello"}` + "\n" + `{"event":"turn_end","stop_reason":"END_TURN","context_pct":0,"model":"fake","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n" + `{"event":"usage_delta","input_tokens":0,"output_tokens":0,"total_tokens":0}` + "\n"
	if w.String() != want {
		t.Errorf("prompt output mismatch:\n got  %q\n want %q", w.String(), want)
	}
}

func TestHandleLineHook(t *testing.T) {
	dir := t.TempDir()
	hookDir := filepath.Join(dir, ".llm-bridge", "hooks")
	if err := os.MkdirAll(hookDir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(hookDir, "algo.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"hook output: $@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	st := newTestState()
	st.cwd = dir
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"#algo parametro"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("hook should not quit")
	}
	// Hooks run asynchronously: handleLine returns "" immediately and the
	// hook_action event is written to w by the hook goroutine when the script
	// finishes. Wait for it to appear (with a timeout).
	deadline := time.After(2 * time.Second)
	for !strings.Contains(w.String(), `"event":"hook_action"`) {
		select {
		case <-deadline:
			t.Fatalf("hook_action never emitted; output=%q", w.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !strings.Contains(w.String(), `"name":"algo"`) ||
		!strings.Contains(w.String(), "hook output: parametro") {
		t.Errorf("hook response mismatch: %q", w.String())
	}
	// The hook must not touch the conversation history.
	if st.historyLen() != 0 {
		t.Errorf("hook should not touch history, got %d messages", st.historyLen())
	}
}

func TestHandleLineHookMissing(t *testing.T) {
	st := newTestState()
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"#naoexiste"}}`
	resp, _ := handleLine(line, st, &w)
	if resp != "" {
		t.Fatalf("expected empty returned response for async hook, got %q", resp)
	}
	// wait for the hook_action error event to be written asynchronously
	deadline := time.After(2 * time.Second)
	for !strings.Contains(w.String(), `"event":"hook_action"`) {
		select {
		case <-deadline:
			t.Fatalf("hook_action never emitted; output=%q", w.String())
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !strings.Contains(w.String(), `"name":"naoexiste"`) ||
		!strings.Contains(w.String(), `"error"`) {
		t.Errorf("missing hook response mismatch: %q", w.String())
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
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

// TestHandleLineCancelPreservesHistory verifies that cancelling does NOT roll
// back the conversation: the user prompt of the in-flight turn (and any partial
// model output) stays in the history so the user keeps the context of what was
// asked and what was in progress.
func TestHandleLineCancelPreservesHistory(t *testing.T) {
	st := newTestState()
	st.history = []llm.Message{
		{Role: llm.RoleUser, Content: "first"},
		{Role: llm.RoleUser, Content: "second"},
	}
	st.historyLenBeforeTurn = 1
	var w bytes.Buffer
	resp, _ := handleLine(`{"method":"cancel"}`, st, &w)
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}
	// history is preserved (both the pre-turn messages and the in-flight
	// prompt "second" that was appended at turn start).
	if len(st.history) != 2 ||
		st.history[0].Content != "first" ||
		st.history[1].Content != "second" {
		t.Fatalf("history should be preserved on cancel, got: %+v", st.history)
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

// TestProjectKBNameMatchesCwdByPath verifies that projectKBName links a base to
// the current cwd when the base path equals/contains the cwd.
func TestProjectKBNameMatchesCwdByPath(t *testing.T) {
	st := &state{
		cwd: "/home/user/proj",
		knowledgeBases: []json.RawMessage{
			json.RawMessage(`{"name":"Projeto X","path":"/home/user/proj"}`),
		},
	}
	if got := st.projectKBName(); got != "Projeto X" {
		t.Fatalf("projectKBName() = %q, want Projeto X", got)
	}
}

// TestProjectKBNameMatchesByProjectName verifies that projectKBName falls back
// to matching the project name (last path element) when the base path is
// unrelated but shares the basename with the cwd.
func TestProjectKBNameMatchesByProjectName(t *testing.T) {
	st := &state{
		cwd: "/other/repo/myproj",
		knowledgeBases: []json.RawMessage{
			json.RawMessage(`{"name":"myproj KB","path":"/data/kbs/myproj"}`),
		},
	}
	if got := st.projectKBName(); got != "myproj KB" {
		t.Fatalf("projectKBName() = %q, want myproj KB", got)
	}
}

// TestProjectKBNameIgnoresOtherProjects verifies that bases belonging to other
// projects do not match, and that a malformed entry is skipped.
func TestProjectKBNameIgnoresOtherProjects(t *testing.T) {
	st := &state{
		cwd: "/home/user/proj",
		knowledgeBases: []json.RawMessage{
			json.RawMessage(`{"name":"Other","path":"/home/user/other"}`),
			json.RawMessage(`not-json`),
		},
	}
	if got := st.projectKBName(); got != "" {
		t.Fatalf("projectKBName() = %q, want empty for non-matching bases", got)
	}
}

// TestProjectKBNameNoCwdOrEmpty verifies the empty-result edge cases: no cwd, no
// bases, or a base without a path.
func TestProjectKBNameNoCwdOrEmpty(t *testing.T) {
	if got := (&state{}).projectKBName(); got != "" {
		t.Fatalf("projectKBName() with no cwd = %q, want empty", got)
	}
	st := &state{
		cwd:            "/home/user/proj",
		knowledgeBases: []json.RawMessage{json.RawMessage(`{"name":"nopath"}`)},
	}
	if got := st.projectKBName(); got != "" {
		t.Fatalf("projectKBName() with pathless base = %q, want empty", got)
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

// TestHandleLineToolResultAfterCancelIgnored verifies that a tool_result that
// arrives for a turn that was already cancelled is silently ignored instead of
// erroring. Some clients (e.g. when a user denies a tool) queue a tool_result
// right before their own cancel, and the reader goroutine may process the
// cancel before the main loop reaches the queued tool_result — erroring there
// would surface a confusing "tool_result without pending tool call".
func TestHandleLineToolResultAfterCancelIgnored(t *testing.T) {
	st := newTestState()
	// simulate an in-flight tool cycle awaiting a tool result
	st.inToolCycle = true
	st.pendingToolIDs = []string{"shell"}
	st.pendingToolResults = map[string]json.RawMessage{}

	// cancel clears the tool cycle and marks the turn cancelled
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}
	if st.inToolCycle || len(st.pendingToolIDs) != 0 {
		t.Fatal("cancel should clear the pending tool cycle")
	}

	// a tool_result for the cancelled turn is silently ignored (no error)
	w.Reset()
	resp, quit = handleLine(`{"method":"tool_result","params":{"id":"shell","result":"user denied execution"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if resp != "" {
		t.Fatalf("expected tool_result after cancel to be ignored, got %q", resp)
	}
	if w.Len() != 0 {
		t.Fatalf("expected no output for ignored tool_result, got %q", w.String())
	}
}

// TestPromptSendsSystemPrompt verifies that every request carries the bridge's
// system prompt, which tells the model not to call tools for casual
// conversation (the fix for the "oi → git status" behavior on agentic models).
func TestPromptSendsSystemPrompt(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"oi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.System == "" {
		t.Fatal("expected system prompt in ChatRequest")
	}
	if !strings.Contains(fp.lastReq.System, "do NOT call a tool") {
		t.Errorf("system prompt should instruct not to call tools for conversation, got %q", fp.lastReq.System)
	}
}

// TestRunWithProviderNil covers the `runWithProvider` branch that starts the
// server with no provider at all (provider == nil): it still emits the
// `ready` event and returns cleanly on EOF.
func TestRunWithProviderNil(t *testing.T) {
	var out bytes.Buffer
	err := runWithProvider(strings.NewReader(""), &out, nil)
	if err != nil {
		t.Fatalf("runWithProvider(nil): %v", err)
	}
	want := `{"event":"ready"}` + "\n"
	if out.String() != want {
		t.Errorf("runWithProvider(nil) output:\n got: %q\nwant: %q", out.String(), want)
	}
}

// TestActiveProviderFallsBackToAnyRegistered covers the fallback branch of
// `activeProvider`: when a provider registry is set but the current
// providerName does not match any registered key, it falls back to returning
// one of the registered providers (instead of returning nil).
func TestActiveProviderFallsBackToAnyRegistered(t *testing.T) {
	ds := &fakeProvider{name: "deepseek"}
	gl := &fakeProvider{name: "google"}
	st := &state{
		providers:    map[string]llm.LLMProvider{"deepseek": ds, "google": gl},
		providerName: "missing", // not in the registry → fall back to any
	}
	got := st.activeProvider()
	if got != ds && got != gl {
		t.Fatalf("activeProvider() = %v, want one of the registered providers (deepseek/google)", got)
	}
	// with a single-provider registry the fallback is deterministic
	st2 := &state{
		providers:    map[string]llm.LLMProvider{"deepseek": ds},
		providerName: "missing",
	}
	if st2.activeProvider() != ds {
		t.Fatalf("activeProvider() single-registry fallback = %v, want deepseek", st2.activeProvider())
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
{"method":"prompt","params":{"text":"hi"}}
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
	line := `{"method":"prompt","params":{"text":"hi"}}`
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
	resp, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
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
	resp, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
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

// FASE 3 — integration tests for context injection

func TestFirstPromptInjectsContextInOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// general context and local context
	writeTestFile(t, filepath.Join(home, ".llm-bridge", "a.md"), "GENERAL")
	writeTestFile(t, filepath.Join(cwd, ".llm-bridge", "b.md"), "LOCAL")

	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp, cwd: cwd, firstTurn: true}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}

	msgs := fp.lastReq.Messages
	if len(msgs) != 3 {
		t.Fatalf("len = %d, want 3 (general, local, prompt)", len(msgs))
	}
	// general before local, prompt last
	if !strings.Contains(msgs[0].Content, "GENERAL") {
		t.Errorf("msgs[0] should contain general context, got %q", msgs[0].Content)
	}
	if !strings.Contains(msgs[1].Content, "LOCAL") {
		t.Errorf("msgs[1] should contain local context, got %q", msgs[1].Content)
	}
	if msgs[2].Role != llm.RoleUser || msgs[2].Content != "hi" {
		t.Errorf("msgs[2] should be the user prompt, got %+v", msgs[2])
	}
}

func TestContextPersistsOnSecondTurn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeTestFile(t, filepath.Join(home, ".llm-bridge", "a.md"), "GENERAL")
	writeTestFile(t, filepath.Join(cwd, ".llm-bridge", "b.md"), "LOCAL")

	fp := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
			{Content: "Again", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: fp, cwd: cwd, firstTurn: true}
	var w bytes.Buffer

	// first prompt
	_, quit := handleLine(`{"method":"prompt","params":{"text":"first"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	firstMsgs := fp.lastReq.Messages
	if len(firstMsgs) != 3 {
		t.Fatalf("first request len = %d, want 3", len(firstMsgs))
	}

	w.Reset()
	// second prompt
	_, quit = handleLine(`{"method":"prompt","params":{"text":"second"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	secondMsgs := fp.lastReq.Messages
	if len(secondMsgs) != 5 {
		t.Fatalf("second request len = %d, want 5 (general, local, first, assistant, second)", len(secondMsgs))
	}
	// context is still present at the start (not ephemeral)
	if !strings.Contains(secondMsgs[0].Content, "GENERAL") {
		t.Errorf("second request msgs[0] should contain general context, got %q", secondMsgs[0].Content)
	}
	if !strings.Contains(secondMsgs[1].Content, "LOCAL") {
		t.Errorf("second request msgs[1] should contain local context, got %q", secondMsgs[1].Content)
	}
	last := secondMsgs[len(secondMsgs)-1]
	if last.Role != llm.RoleUser || last.Content != "second" {
		t.Errorf("last message should be the second prompt, got %+v", last)
	}
}

func TestCancelPreservesContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	writeTestFile(t, filepath.Join(home, ".llm-bridge", "a.md"), "GENERAL")
	writeTestFile(t, filepath.Join(cwd, ".llm-bridge", "b.md"), "LOCAL")

	// provider returns a tool call so the turn stays in progress and the
	// historyLenBeforeTurn marker is not reset by a completed turn
	st := &state{
		firstTurn: true,
		provider: &fakeProvider{
			name: "fake",
			resp: &llm.ChatResponse{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`},
				},
			},
		},
	}
	st.cwd = cwd
	var w bytes.Buffer

	// prompt injects context and enters the tool cycle: 2 context + 1 prompt
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle after prompt with tool calls")
	}
	if len(st.history) != 4 {
		t.Fatalf("history len before cancel = %d, want 4 (2 context + 1 prompt + 1 assistant)", len(st.history))
	}

	// cancel while awaiting the tool result
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}

	// context AND the in-flight user prompt are preserved on cancel, but the
	// orphaned assistant message with tool_calls (which never received its tool
	// result) is sanitized so the next request is not rejected by the API.
	if len(st.history) != 3 {
		t.Fatalf("history len after cancel = %d, want 3 (2 context + 1 prompt; orphan tool_calls dropped)", len(st.history))
	}
	if !strings.Contains(st.history[0].Content, "GENERAL") {
		t.Errorf("history[0] should contain general context, got %q", st.history[0].Content)
	}
	if !strings.Contains(st.history[1].Content, "LOCAL") {
		t.Errorf("history[1] should contain local context, got %q", st.history[1].Content)
	}
	last := st.history[len(st.history)-1]
	if last.Role != llm.RoleUser || last.Content != "hi" {
		t.Errorf("last message should be the original user prompt, got %+v", last)
	}
	for _, m := range st.history {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("orphaned assistant tool_calls message should be removed on cancel, got %+v", m)
		}
	}
}

// TestPromptAfterCancelDuringToolCycle verifies the fix for the HTTP 400 the
// API returns when an assistant message with tool_calls is sent back without the
// corresponding tool messages. After cancelling a prompt that was awaiting a
// tool result, a new prompt must be sent WITHOUT the orphaned tool_calls
// message; otherwise the provider rejects the request (the exact error the user
// hit after a stuck process left an incomplete tool cycle).
func TestPromptAfterCancelDuringToolCycle(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
			{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider}
	var w bytes.Buffer

	// first prompt enters the tool cycle and leaves the assistant tool_calls
	// pending (no tool_result delivered yet)
	_, quit := handleLine(`{"method":"prompt","params":{"text":"first"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle")
	}

	// cancel mid-tool-cycle (the user's stuck process scenario)
	resp, _ := handleLine(`{"method":"cancel"}`, st, &w)
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}

	// the orphaned assistant tool_calls message must have been sanitized away
	for _, m := range st.history {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("orphaned tool_calls left in history after cancel: %+v", m)
		}
	}

	// a new prompt must reach the provider WITHOUT any orphaned tool_calls
	provider.callCount = 0
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"second"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	for _, m := range provider.lastReq.Messages {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("new prompt sent orphaned tool_calls to provider: %+v", m)
		}
	}
}

func TestNoContextFilesBehavesAsBefore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := t.TempDir()

	// no .llm-bridge files in HOME or cwd
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp, cwd: cwd, firstTurn: true}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}

	msgs := fp.lastReq.Messages
	if len(msgs) != 1 {
		t.Fatalf("len = %d, want 1 (prompt only)", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || msgs[0].Content != "hi" {
		t.Fatalf("unexpected message: %+v", msgs[0])
	}
}

func TestHandleLinePromptThinkingOverride(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","thinking":false}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.Thinking == nil {
		t.Fatal("expected Thinking override in ChatRequest")
	}
	if *fp.lastReq.Thinking {
		t.Fatal("Thinking = true, want false")
	}
	if fp.lastReq.Model != "" {
		t.Errorf("Model = %q, want empty", fp.lastReq.Model)
	}
	// the override persists for subsequent prompts without it
	fp.lastReq = llm.ChatRequest{}
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"again"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.Thinking == nil || *fp.lastReq.Thinking {
		t.Fatalf("expected persisted Thinking=false, got %+v", fp.lastReq.Thinking)
	}
}

func TestHandleLinePromptModelOverride(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","model":"deepseek-reasoner"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.Model != "deepseek-reasoner" {
		t.Errorf("Model = %q, want deepseek-reasoner", fp.lastReq.Model)
	}
	if fp.lastReq.Thinking != nil {
		t.Errorf("Thinking should be nil, got %+v", fp.lastReq.Thinking)
	}
}

func TestHandleLinePromptReasoningEffortOverride(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","reasoning_effort":"low"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.ReasoningEffort == nil {
		t.Fatal("expected ReasoningEffort override in ChatRequest")
	}
	if *fp.lastReq.ReasoningEffort != "low" {
		t.Fatalf("ReasoningEffort = %q, want low", *fp.lastReq.ReasoningEffort)
	}
	if fp.lastReq.Thinking != nil {
		t.Errorf("Thinking should be nil, got %+v", fp.lastReq.Thinking)
	}
	// the override persists for subsequent prompts without it
	fp.lastReq = llm.ChatRequest{}
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"again"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.ReasoningEffort == nil || *fp.lastReq.ReasoningEffort != "low" {
		t.Fatalf("expected persisted ReasoningEffort=low, got %+v", fp.lastReq.ReasoningEffort)
	}
}

// TestHandleLinePromptProviderOverride verifies that a prompt can switch the
// active provider via the `provider` field, that the switch persists for
// subsequent prompts without it, and that unknown providers are rejected.
func TestHandleLinePromptProviderOverride(t *testing.T) {
	ds := &fakeProvider{
		name:      "deepseek",
		responses: []*llm.ChatResponse{{Content: "ds", StopReason: "END_TURN", Usage: &llm.Usage{}}},
	}
	gl := &fakeProvider{
		name:      "google",
		responses: []*llm.ChatResponse{{Content: "gl", StopReason: "END_TURN", Usage: &llm.Usage{}}},
	}
	st := &state{
		providers:    map[string]llm.LLMProvider{"deepseek": ds, "google": gl},
		providerName: "deepseek",
	}
	var w bytes.Buffer

	// switch to google with a model override
	line := `{"method":"prompt","params":{"text":"hi","provider":"google","model":"gemini-x"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if gl.callCount != 1 {
		t.Fatalf("google provider callCount = %d, want 1", gl.callCount)
	}
	if ds.callCount != 0 {
		t.Fatalf("deepseek provider callCount = %d, want 0", ds.callCount)
	}
	if gl.lastReq.Model != "gemini-x" {
		t.Errorf("google Model = %q, want gemini-x", gl.lastReq.Model)
	}
	if !strings.Contains(w.String(), `"text":"gl"`) {
		t.Errorf("expected google chunk in output, got %q", w.String())
	}

	// the provider switch persists for the next prompt without a provider field
	gl.callCount = 0
	ds.callCount = 0
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"again"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if gl.callCount != 1 || ds.callCount != 0 {
		t.Fatalf("expected persisted google provider (gl=%d ds=%d)", gl.callCount, ds.callCount)
	}

	// switching back to deepseek works too
	gl.callCount = 0
	ds.callCount = 0
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"back","provider":"deepseek"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if ds.callCount != 1 || gl.callCount != 0 {
		t.Fatalf("expected switch back to deepseek (gl=%d ds=%d)", gl.callCount, ds.callCount)
	}
}

// TestHandleLinePromptProviderUnknown verifies that a prompt asking for an
// unknown provider is rejected before any provider is called.
func TestHandleLinePromptProviderUnknown(t *testing.T) {
	ds := &fakeProvider{
		name: "deepseek",
		resp: &llm.ChatResponse{Content: "ds", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{
		providers:    map[string]llm.LLMProvider{"deepseek": ds},
		providerName: "deepseek",
	}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","provider":"anthropic"}}`
	resp, _ := handleLine(line, st, &w)
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "unknown provider: anthropic") {
		t.Fatalf("expected unknown provider error, got %q", resp)
	}
	if ds.callCount != 0 {
		t.Fatalf("provider should not be called for unknown provider, got %d calls", ds.callCount)
	}
}

// TestHandleLinePromptProviderNotAvailable verifies that asking to switch
// provider on a server started with a single fixed provider is rejected.
func TestHandleLinePromptProviderNotAvailable(t *testing.T) {
	st := &state{
		provider: &fakeProvider{
			name: "deepseek",
			resp: &llm.ChatResponse{Content: "ds", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","provider":"google"}}`
	resp, _ := handleLine(line, st, &w)
	if !strings.Contains(resp, `"event":"error"`) || !strings.Contains(resp, "provider switching not available: google") {
		t.Fatalf("expected provider switching not available error, got %q", resp)
	}
}

func TestTurnEndUsesResponseModel(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{
			Content:    "Hello",
			StopReason: "END_TURN",
			Usage:      &llm.Usage{},
			Model:      "deepseek-reasoner",
		},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !strings.Contains(w.String(), `"model":"deepseek-reasoner"`) {
		t.Fatalf("expected resolved model in turn_end, got %q", w.String())
	}
}

// writeTestFile creates the file (and parent directories) with the given content.
func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestHandleLinePromptEmitsThinkingEvent(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{
			Content:    "answer",
			StopReason: "END_TURN",
			Usage:      &llm.Usage{},
			Reasoning:  "let me think about this",
		},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	out := w.String()
	if !strings.Contains(out, `{"event":"thinking","text":"let me think about this"}`) {
		t.Fatalf("expected thinking event with reasoning, got %q", out)
	}
	// reasoning is NOT kept in the history for a message without tool calls (it
	// is never sent back to the provider in that case, so storing it would be
	// dead weight).
	if len(st.history) == 0 || st.history[len(st.history)-1].Reasoning != "" {
		t.Fatalf("expected reasoning dropped from non-tool-call history message, got %+v", st.history)
	}
}

// TestHandleLinePromptEmitsThinkingFallback cobre o branch do runToolCycle em que
// o provedor retorna resp.Reasoning sem streameá-lo (OnReasoning nunca é chamado,
// via fakeProvider.skipReasoning). Nesse caso o servidor emite um único evento
// thinking como fallback e ainda persiste o reasoning no histórico.
func TestHandleLinePromptEmitsThinkingFallback(t *testing.T) {
	fp := &fakeProvider{
		name:          "fake",
		skipReasoning: true,
		resp: &llm.ChatResponse{
			Content:    "answer",
			StopReason: "END_TURN",
			Usage:      &llm.Usage{},
			Reasoning:  "think step by step",
		},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	out := w.String()
	// exactly ONE thinking event, via fallback (not streaming)
	if got := strings.Count(out, `"event":"thinking"`); got != 1 {
		t.Fatalf("expected exactly 1 thinking event (fallback), got %d in %q", got, out)
	}
	if !strings.Contains(out, `"text":"think step by step"`) {
		t.Fatalf("expected fallback thinking event with reasoning text, got %q", out)
	}
	// the reasoning is NOT persisted for a message without tool calls (it is
	// never re-sent to the provider in that case).
	if len(st.history) == 0 || st.history[len(st.history)-1].Reasoning != "" {
		t.Fatalf("expected reasoning dropped from non-tool-call history message, got %+v", st.history)
	}
}

// blockingProvider simulates a long-running streaming model: Chat blocks until
// the request context is cancelled, exactly like a real stream that has not
// finished speaking yet.
type blockingProvider struct {
	started chan struct{}
	once    sync.Once
}

func (b *blockingProvider) Chat(ctx context.Context, _ llm.ChatRequest, _ func(string)) (*llm.ChatResponse, error) {
	b.once.Do(func() { close(b.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func (b *blockingProvider) Name() string  { return "block" }
func (b *blockingProvider) Model() string { return "block" }

// partialStreamProvider streams some thinking/content and then blocks until the
// request context is cancelled, simulating a real model interrupted mid-answer.
// This lets tests assert that the partial output is preserved on cancel.
type partialStreamProvider struct {
	started   chan struct{}
	onChunk   func(string)
	once      sync.Once
	cancelled chan struct{}
}

func (p *partialStreamProvider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(string)) (*llm.ChatResponse, error) {
	p.once.Do(func() { close(p.started) })
	p.onChunk = onChunk
	// Stream a thinking event via the server's OnReasoning callback (the real
	// chain-of-thought streaming path), then a content chunk, and finally block
	// until the request context is cancelled.
	if req.OnReasoning != nil {
		req.OnReasoning("thinking about it")
	}
	onChunk("partial answer")
	<-ctx.Done()
	close(p.cancelled)
	return nil, ctx.Err()
}

func (p *partialStreamProvider) Name() string  { return "partial" }
func (p *partialStreamProvider) Model() string { return "partial" }

// TestCancelPreservesPartialOutput verifies that when a turn is cancelled, the
// user prompt AND the partial thinking/content the model produced before being
// interrupted are kept in the history, so the next prompt retains the context
// of what was asked and what was in progress.
func TestCancelPreservesPartialOutput(t *testing.T) {
	provider := &partialStreamProvider{
		started:   make(chan struct{}),
		cancelled: make(chan struct{}),
	}
	st := &state{provider: provider}
	var w bytes.Buffer

	// The provider's Chat streams a thinking event + one content chunk and then
	// blocks until the request context is cancelled. Start the prompt in a
	// goroutine: it must NOT return until we cancel the in-flight turn.
	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		if _, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w); quit {
			t.Error("prompt should not quit")
		}
	}()

	// Wait until the provider is streaming (Chat has delivered partial output
	// and is now blocked).
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never started Chat")
	}

	// Cancel the in-flight turn now.
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}

	// The prompt turn must unwind once the stream is cancelled.
	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt handleLine never returned after cancel")
	}

	// The partial thinking + content the model streamed in real time before the
	// cancel were surfaced to the client as normal events (the `cancelled` ack
	// was written by the cancel handler, not by the prompt turn).
	out := w.String()
	if !strings.Contains(out, `{"event":"thinking","text":"thinking about it"}`) {
		t.Fatalf("expected streamed thinking event before cancel, got %q", out)
	}
	if !strings.Contains(out, `{"event":"chunk","text":"partial answer"}`) {
		t.Fatalf("expected streamed chunk event before cancel, got %q", out)
	}

	// the user prompt and the partial assistant (thinking + content) are kept
	if len(st.history) != 2 {
		t.Fatalf("history len = %d, want 2 (prompt + partial assistant)", len(st.history))
	}
	if st.history[0].Role != llm.RoleUser || st.history[0].Content != "hi" {
		t.Fatalf("history[0] should be the user prompt, got %+v", st.history[0])
	}
	last := st.history[1]
	if last.Role != llm.RoleAssistant {
		t.Fatalf("history[1] should be the partial assistant message, got %+v", last)
	}
	if last.Content != "partial answer" {
		t.Fatalf("partial content not preserved, got %q", last.Content)
	}
	// the partial reasoning (no tool calls) is NOT kept in the history: it is
	// never re-sent to the provider for a non-tool-call message, so storing it
	// would be dead weight.
	if last.Reasoning != "" {
		t.Fatalf("expected partial reasoning dropped from non-tool-call history message, got %q", last.Reasoning)
	}
}

// syncBuffer is a bytes.Buffer safe for concurrent read/write, so the test can
// poll the server's output while the reader goroutine is writing to it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestCancelInterruptsStreamingChat verifies that a `cancel` command is honored
// immediately while a prompt's Chat is still streaming, instead of only after
// the model finishes. The blocking provider never returns on its own, so if the
// server waited for the stream to end the `cancelled` event would never appear
// and this test would time out.
func TestCancelInterruptsStreamingChat(t *testing.T) {
	provider := &blockingProvider{started: make(chan struct{})}
	pr, pw := io.Pipe()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- runWithProvider(pr, out, provider) }()

	// start a prompt; the provider blocks in Chat until cancelled
	if _, err := fmt.Fprintln(pw, `{"method":"prompt","params":{"text":"hi"}}`); err != nil {
		t.Fatalf("write prompt: %v", err)
	}
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never started Chat")
	}

	// cancel now: must interrupt the streaming Chat promptly
	if _, err := fmt.Fprintln(pw, `{"method":"cancel"}`); err != nil {
		t.Fatalf("write cancel: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		if strings.Contains(out.String(), `{"event":"cancelled"}`) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("cancel was not honored while Chat was streaming; output=%q", out.String())
		case <-time.After(10 * time.Millisecond):
		}
	}

	// shut down: closing the write end signals EOF so the reader goroutine
	// finishes cleanly (no more commands are expected after the cancel).
	_ = pw.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWithProvider did not return after cancel")
	}
	_ = pr.Close()
}

func TestHandleLinePromptSystemOverride(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	line := `{"method":"prompt","params":{"text":"hi","system":"custom system prompt"}}`
	_, quit := handleLine(line, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.System != "custom system prompt" {
		t.Errorf("System = %q, want custom system prompt", fp.lastReq.System)
	}

	// override persists for subsequent prompts without it
	fp.lastReq = llm.ChatRequest{}
	w.Reset()
	_, quit = handleLine(`{"method":"prompt","params":{"text":"again"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if fp.lastReq.System != "custom system prompt" {
		t.Errorf("persisted System = %q, want custom system prompt", fp.lastReq.System)
	}
}

// TestAggressivePruneCollapsesToolTurn verifies that with aggressive prune
// enabled, a completed tool-calling turn collapses to just the user prompt +
// the final assistant answer, dropping the intermediate tool_calls and tool
// results. The prefix (context + prior turns) is preserved byte-identically.
func TestAggressivePruneCollapsesToolTurn(t *testing.T) {
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
				Reasoning:  "thinking that is never re-sent",
			},
		},
	}
	st := &state{provider: provider, aggressivePrune: true}
	// Seed a prior turn (a plain user->assistant pair) that must be preserved.
	st.history = []llm.Message{
		{Role: llm.RoleUser, Content: "earlier"},
		{Role: llm.RoleAssistant, Content: "previous answer"},
	}
	var w bytes.Buffer

	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle")
	}
	w.Reset()
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"file content"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}

	// history is now [earlier, previous answer, hi(user), final answer(done)]
	// — the intermediate assistant(tool_calls) and the tool result are gone.
	if len(st.history) != 4 {
		t.Fatalf("history len = %d, want 4 (2 prior + user prompt + final answer), got %+v", len(st.history), st.history)
	}
	if st.history[0].Content != "earlier" || st.history[1].Content != "previous answer" {
		t.Fatalf("prior turns should be preserved untouched, got %+v", st.history[:2])
	}
	if st.history[2].Role != llm.RoleUser || st.history[2].Content != "hi" {
		t.Fatalf("history[2] should be the user prompt, got %+v", st.history[2])
	}
	last := st.history[3]
	if last.Role != llm.RoleAssistant || last.Content != "done" {
		t.Fatalf("history[3] should be the final answer, got %+v", last)
	}
	for _, m := range st.history {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("collapsed history should contain no tool_calls, got %+v", m)
		}
	}
}

// TestAggressivePruneKeepsConversationTurns verifies that aggressive prune does
// NOT touch plain conversational turns (no tools): the user prompt and the
// assistant answer are kept as-is, so nothing is lost or reordered.
func TestAggressivePruneKeepsConversationTurns(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp, aggressivePrune: true}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if len(st.history) != 2 {
		t.Fatalf("history len = %d, want 2 (prompt + answer)", len(st.history))
	}
	if st.history[0].Role != llm.RoleUser || st.history[0].Content != "hi" {
		t.Fatalf("history[0] should be the user prompt, got %+v", st.history[0])
	}
	if st.history[1].Role != llm.RoleAssistant || st.history[1].Content != "Hello" {
		t.Fatalf("history[1] should be the assistant answer, got %+v", st.history[1])
	}
}

// TestAggressivePruneDefaultOff verifies that WITHOUT the flag the interleaved
// history (assistant tool_calls + tool result) is preserved across the turn,
// matching the previous behavior.
func TestAggressivePruneDefaultOff(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider} // aggressivePrune defaults to false
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	w.Reset()
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"file content"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	// [user, assistant(tool_calls), tool, assistant(final)] — nothing collapsed.
	if len(st.history) != 4 {
		t.Fatalf("history len = %d, want 4 (interleaved, prune off), got %+v", len(st.history), st.history)
	}
	if len(st.history[1].ToolCalls) != 1 || st.history[2].Role != "tool" {
		t.Fatalf("interleaved tool history should be preserved without prune, got %+v", st.history)
	}
}

// ---------------------------------------------------------------------------
// Helper unit tests (recordFileChanged, parseHook, filePathFromArgs)
// ---------------------------------------------------------------------------

// TestRecordFileChanged verifies that recordFileChanged ignores an empty path,
// appends new paths, and dedups (a path already in the current turn's list is
// not added again).
func TestRecordFileChanged(t *testing.T) {
	st := &state{}
	st.recordFileChanged("")
	if len(st.turnFilesChanged) != 0 {
		t.Fatalf("empty path should be ignored, got %v", st.turnFilesChanged)
	}
	st.recordFileChanged("/tmp/a.go")
	st.recordFileChanged("/tmp/b.go")
	if len(st.turnFilesChanged) != 2 ||
		st.turnFilesChanged[0] != "/tmp/a.go" ||
		st.turnFilesChanged[1] != "/tmp/b.go" {
		t.Fatalf("expected [a.go b.go], got %v", st.turnFilesChanged)
	}
	// duplicate is ignored
	st.recordFileChanged("/tmp/a.go")
	if len(st.turnFilesChanged) != 2 {
		t.Fatalf("duplicate path should be deduped, got %v", st.turnFilesChanged)
	}
}

// TestParseHook verifies that parseHook splits a hook message (starting with
// "#") into the hook name and its remaining arguments, and that a bare "#"
// yields an empty name with no args.
func TestParseHook(t *testing.T) {
	cases := []struct {
		in   string
		name string
		args []string
	}{
		{"#ls -l", "ls", []string{"-l"}},
		{"#algo", "algo", nil},
		{"#", "", nil},
		{"#ls  -l  --all", "ls", []string{"-l", "--all"}},
	}
	for _, tc := range cases {
		name, args := parseHook(tc.in)
		if name != tc.name {
			t.Errorf("parseHook(%q) name = %q, want %q", tc.in, name, tc.name)
		}
		if len(args) != len(tc.args) {
			t.Errorf("parseHook(%q) args = %v, want %v", tc.in, args, tc.args)
			continue
		}
		for i := range args {
			if args[i] != tc.args[i] {
				t.Errorf("parseHook(%q) args[%d] = %q, want %q", tc.in, i, args[i], tc.args[i])
			}
		}
	}
}

// TestFilePathFromArgs verifies that filePathFromArgs extracts the "path"
// argument of a tool call's JSON arguments, and returns "" for an empty string,
// unparseable JSON, or JSON without a path field.
func TestFilePathFromArgs(t *testing.T) {
	if got := filePathFromArgs(`{"path":"/tmp/x"}`); got != "/tmp/x" {
		t.Errorf("filePathFromArgs(valid) = %q, want /tmp/x", got)
	}
	if got := filePathFromArgs(""); got != "" {
		t.Errorf("filePathFromArgs(empty) = %q, want empty", got)
	}
	if got := filePathFromArgs("not json"); got != "" {
		t.Errorf("filePathFromArgs(invalid) = %q, want empty", got)
	}
	if got := filePathFromArgs(`{"other":1}`); got != "" {
		t.Errorf("filePathFromArgs(no path) = %q, want empty", got)
	}
}

func TestRunWithSystemPromptGlobal(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	var out bytes.Buffer
	input := `{"method":"prompt","params":{"text":"hi"}}
{"method":"quit"}
`
	err := RunWithSystemPrompt(strings.NewReader(input), &out, map[string]llm.LLMProvider{"fake": fp}, "fake", "global system prompt")
	if err != nil {
		t.Fatalf("RunWithSystemPrompt error: %v", err)
	}
	if fp.lastReq.System != "global system prompt" {
		t.Errorf("System = %q, want global system prompt", fp.lastReq.System)
	}
}

// ---------------------------------------------------------------------------
// Knowledge base (resolveKnowledge + first-turn injection) tests
// ---------------------------------------------------------------------------

// TestResolveKnowledgeDisabled verifies that when no knowledge base is wired
// (kb nil), a `knowledge` tool call resolves internally to a "disabled" result
// appended to the history, instead of waiting on the client.
func TestResolveKnowledgeDisabled(t *testing.T) {
	st := newTestState()
	st.resolveKnowledge("call_1", `{"command":"show"}`)
	if len(st.history) != 1 {
		t.Fatalf("history len = %d, want 1 (tool result)", len(st.history))
	}
	last := st.history[len(st.history)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" || last.Content != "knowledge base disabled" {
		t.Fatalf("expected disabled tool result, got %+v", last)
	}
}

// TestResolveKnowledgeExecuteError verifies that a `knowledge` tool call against
// a wired-but-disabled embedder resolves internally to an error result (an
// embed that fails still produces a tool result so the model keeps its turn).
func TestResolveKnowledgeExecuteError(t *testing.T) {
	kb := knowledge.NewManager(knowledge.DisabledEmbedder{}, "proj")
	st := &state{kb: kb}
	st.resolveKnowledge("call_1", `{"command":"add","label":"x","text":"y"}`)
	last := st.history[len(st.history)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" {
		t.Fatalf("expected tool result, got %+v", last)
	}
	if !strings.Contains(last.Content, "error:") {
		t.Fatalf("expected error result from disabled embedder, got %q", last.Content)
	}
}

// TestResolveKnowledgeNilArgs verifies that a knowledge tool call with empty
// arguments still resolves internally without panicking (args omitted → empty
// JSON payload).
func TestResolveKnowledgeNilArgs(t *testing.T) {
	kb := knowledge.NewManager(knowledge.DisabledEmbedder{}, "proj")
	st := &state{kb: kb}
	st.resolveKnowledge("call_1", "")
	last := st.history[len(st.history)-1]
	if last.Role != "tool" || last.ToolCallID != "call_1" {
		t.Fatalf("expected tool result, got %+v", last)
	}
}

// TestFirstTurnInjectsKnowledgeBaseLine verifies that when the knowledge base is
// wired (kb != nil), the first prompt injects a single minimal line about the
// project's KB before the user prompt. The KB name falls back to the cwd
// basename when no project metadata is linked.
func TestFirstTurnInjectsKnowledgeBaseLine(t *testing.T) {
	kb := knowledge.NewManager(knowledge.DisabledEmbedder{}, "proj")
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	t.Setenv("HOME", t.TempDir()) // isolate general context (none injected)
	st := &state{provider: fp, kb: kb, firstTurn: true, cwd: "/home/user/proj"}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	msgs := fp.lastReq.Messages
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2 (KB line + prompt)", len(msgs))
	}
	if !strings.Contains(msgs[0].Content, "Knowledge base") || !strings.Contains(msgs[0].Content, "proj") {
		t.Fatalf("msgs[0] should be the KB line, got %q", msgs[0].Content)
	}
	if msgs[1].Role != llm.RoleUser || msgs[1].Content != "hi" {
		t.Fatalf("msgs[1] should be the user prompt, got %+v", msgs[1])
	}
}

// TestKnowledgeToolResolvedInternally verifies that a `knowledge` tool call is
// resolved internally (fire-and-forget) and never enters pendingToolIDs: the
// turn keeps moving and Chat is called again without any client round-trip.
func TestKnowledgeToolResolvedInternally(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "k1", Name: "knowledge", Arguments: `{"command":"add","label":"n","text":"t"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider, kb: knowledge.NewManager(knowledge.DisabledEmbedder{}, "proj")}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"remember"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	// knowledge resolved internally → Chat called twice (tool call + final),
	// with no pending tool and no tool cycle left behind.
	if provider.callCount != 2 {
		t.Fatalf("callCount = %d, want 2 (no client round-trip)", provider.callCount)
	}
	if st.inToolCycle {
		t.Fatal("should not be in tool cycle after internal knowledge resolution")
	}
	if len(st.pendingToolIDs) != 0 {
		t.Fatalf("knowledge tool should not be pending, got %v", st.pendingToolIDs)
	}
	last := st.history[len(st.history)-1]
	if last.Role != llm.RoleAssistant || last.Content != "done" {
		t.Fatalf("final message should be the answer, got %+v", last)
	}
}

// TestRunWithKnowledgeWiresKB verifies the RunWithKnowledgeBase entry point
// wires a knowledge base and resolves a knowledge tool call internally through
// the full server loop (covers the kb != nil logging path too). A set_cwd
// drives the per-project Manager to be loaded for the project.
func TestRunWithKnowledgeWiresKB(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "k1", Name: "knowledge", Arguments: `{"command":"show"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	var out bytes.Buffer
	input := `{"method":"set_cwd","params":{"cwd":"/tmp/proj"}}
{"method":"prompt","params":{"text":"hi"}}
{"method":"quit"}
`
	err := RunWithKnowledgeBase(strings.NewReader(input), &out, map[string]llm.LLMProvider{"fake": provider}, "fake", "", false, false, t.TempDir(), knowledge.DisabledEmbedder{})
	if err != nil {
		t.Fatalf("RunWithKnowledgeBase error: %v", err)
	}
	if provider.callCount != 2 {
		t.Fatalf("callCount = %d, want 2 (internal knowledge resolution)", provider.callCount)
	}
	if !strings.Contains(out.String(), `{"event":"turn_end"`) {
		t.Fatalf("expected turn_end, got %q", out.String())
	}
}

// TestLoadKBProjectLoadsPersistedKB verifies that loadKBProject (called on
// set_cwd) loads the project's persisted data.json into st.kb, so the KB
// populated via -populate-project-kb is actually used at runtime.
func TestLoadKBProjectLoadsPersistedKB(t *testing.T) {
	base := t.TempDir()
	// Persist a KB for project "proj" directly (same schema Load reads).
	dataPath := filepath.Join(base, "proj", "data.json")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte(`[
  {"id":0,"payload":{"label":"seed:README.md","text":"conteudo","vector":[1,0]}},
  {"id":1,"payload":{"label":"seed:proj/README.md","text":"proj","vector":[0,1]}}
]`), 0o644); err != nil {
		t.Fatal(err)
	}

	st := &state{kbBaseDir: base, kbEmbed: knowledge.DisabledEmbedder{}}
	st.loadKBProject("proj")
	if st.kb == nil {
		t.Fatal("expected kb to be loaded for project")
	}
	if st.kb.Len() != 2 {
		t.Fatalf("kb.Len() = %d, want 2", st.kb.Len())
	}
}

// TestLoadKBProjectDisabled verifies that loadKBProject with no embedder/base
// dir clears st.kb (KB disabled) and does not panic.
func TestLoadKBProjectDisabled(t *testing.T) {
	st := &state{kb: knowledge.NewManager(knowledge.DisabledEmbedder{}, "x")}
	st.loadKBProject("proj")
	if st.kb != nil {
		t.Fatalf("expected kb cleared when disabled, got %+v", st.kb)
	}
}

// TestLoadKBProjectEmptyProject verifies that loadKBProject with an empty
// project name is a no-op: it leaves the current kb unchanged (covers the
// early-return guard).
func TestLoadKBProjectEmptyProject(t *testing.T) {
	st := &state{kbBaseDir: t.TempDir(), kbEmbed: knowledge.DisabledEmbedder{}}
	st.loadKBProject("") // no-op, does not panic
	if st.kb != nil {
		t.Fatalf("expected kb untouched for empty project, got %+v", st.kb)
	}
}

// TestLoadKBProjectFallbackOnError verifies that when loading the project's
// data.json fails (corrupt file), loadKBProject falls back to an empty
// in-memory manager for that project so the `knowledge` tool still resolves
// locally instead of erroring (covers the Load-error branch).
func TestLoadKBProjectFallbackOnError(t *testing.T) {
	base := t.TempDir()
	dataPath := filepath.Join(base, "proj", "data.json")
	if err := os.MkdirAll(filepath.Dir(dataPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dataPath, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	st := &state{kbBaseDir: base, kbEmbed: knowledge.DisabledEmbedder{}}
	st.loadKBProject("proj")
	if st.kb == nil {
		t.Fatal("expected kb fallback manager to be set on load error")
	}
	if st.kb.Len() != 0 {
		t.Fatalf("expected empty fallback manager, got %d items", st.kb.Len())
	}
}

// ---------------------------------------------------------------------------
// Prune (-prune) write / replace-only / guard tests
// ---------------------------------------------------------------------------

// TestPruneWriteToolCreatesState verifies that a `write` tool produces a <State>
// snapshot in the pruned history, with the content taken from the write
// argument (the most faithful snapshot of the whole file).
func TestPruneWriteToolCreatesState(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "w1", Name: "write", Arguments: `{"path":"/tmp/new.go","content":"package main"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider, prune: true}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"create file"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle")
	}
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"w1","result":"ok"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if st.inToolCycle {
		t.Fatal("expected tool cycle to finish")
	}
	// [prompt, <State from write content>, answer]
	if len(st.history) != 3 {
		t.Fatalf("history len = %d, want 3 (prompt + State + answer), got %+v", len(st.history), st.history)
	}
	stateMsg := st.history[1]
	if stateMsg.Role != llm.RoleUser || !strings.Contains(stateMsg.Content, "<State /tmp/new.go ") || !strings.Contains(stateMsg.Content, ">package main</State>") {
		t.Fatalf("history[1] should be a <State> from the write content, got %+v", stateMsg)
	}
}

// TestPruneReplaceOnlyAppendsDelta verifies that a search_replace with no prior
// read/write baseline in the turn produces just a <Replace> delta (the baseline
// lives in a prior turn's history), covering the no-state branch of the
// per-path reduction.
func TestPruneReplaceOnlyAppendsDelta(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "s1", Name: "search_replace", Arguments: `{"path":"/tmp/a.go","search":"x","replace":"y"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider, prune: true}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"patch"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle")
	}
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"s1","result":"ok"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if st.inToolCycle {
		t.Fatal("expected tool cycle to finish")
	}
	// [prompt, <Replace>, answer]
	if len(st.history) != 3 {
		t.Fatalf("history len = %d, want 3 (prompt + Replace + answer), got %+v", len(st.history), st.history)
	}
	repMsg := st.history[1]
	if repMsg.Role != llm.RoleUser || !strings.Contains(repMsg.Content, "<Replace /tmp/a.go ") {
		t.Fatalf("history[1] should be a <Replace> delta, got %+v", repMsg)
	}
}

// TestPruneTurnIgnoresUnknownToolResult verifies that a tool result referencing
// an unknown call id is skipped without panicking (covers the `!ok` guard), and
// that a known read still becomes a <State> snapshot.
func TestPruneTurnIgnoresUnknownToolResult(t *testing.T) {
	st := &state{history: []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "known", Name: "read", Arguments: `{"path":"/tmp/a"}`}}},
		{Role: llm.RoleUser, Content: "task"},
		{Role: "tool", ToolCallID: "known", Content: "body"},
		{Role: "tool", ToolCallID: "unknown", Content: "orphan"},
		{Role: llm.RoleAssistant, Content: "done"},
	}}
	st.historyLenBeforeTurn = 0
	st.pruneTurn()
	// [hi(user), <State /tmp/a>, done]
	if len(st.history) != 3 {
		t.Fatalf("history len = %d, want 3 (prompt + State + answer), got %+v", len(st.history), st.history)
	}
	stateMsg := st.history[1]
	if stateMsg.Role != llm.RoleUser || !strings.Contains(stateMsg.Content, "<State /tmp/a ") {
		t.Fatalf("history[1] should be a <State>, got %+v", stateMsg)
	}
}

// TestPruneAndCollapseTurnGuards covers the early-return guard clauses shared by
// collapseTurn and pruneTurn: an invalid start marker and a history that does
// not end with a plain assistant answer (no tool calls) are both left untouched.
func TestPruneAndCollapseTurnGuards(t *testing.T) {
	// start marker beyond history length → guards return early
	st := &state{history: []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "ok"},
	}}
	st.historyLenBeforeTurn = 5
	st.collapseTurn()
	st.pruneTurn()
	if len(st.history) != 2 {
		t.Fatalf("guards should leave history untouched, got %+v", st.history)
	}

	// last message carries tool_calls (not a plain answer) → guards return early
	st2 := &state{history: []llm.Message{
		{Role: llm.RoleUser, Content: "hi"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "c", Name: "read"}}},
	}}
	st2.historyLenBeforeTurn = 0
	st2.collapseTurn()
	st2.pruneTurn()
	if len(st2.history) != 2 {
		t.Fatalf("tool_calls last message should be left untouched, got %+v", st2.history)
	}
}

// TestRunWithOptionsPruneAndAggressive exercises the run() option-logging
// branches for aggressivePrune and prune (reaching the pure logging paths), and
// confirms the turn still completes normally.
func TestRunWithOptionsPruneAndAggressive(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	var out bytes.Buffer
	input := `{"method":"prompt","params":{"text":"hi"}}
{"method":"quit"}
`
	err := RunWithOptions(strings.NewReader(input), &out, map[string]llm.LLMProvider{"fake": fp}, "fake", "", true, true)
	if err != nil {
		t.Fatalf("RunWithOptions error: %v", err)
	}
	if !strings.Contains(out.String(), `{"event":"turn_end"`) {
		t.Fatalf("expected turn_end, got %q", out.String())
	}
}

// ---------------------------------------------------------------------------
// Prune (-prune) tests
// ---------------------------------------------------------------------------

// TestPruneCollapsesToolTurnIntoSnapshots verifies the full -prune flow: a turn
// that reads a file and then applies a search_replace is rebuilt at turn close as
// [prior prefix + user prompt + <State> snapshot + <Replace> delta + final
// answer], with all intermediate tool_calls, tool results, and reasoning dropped
// and no assistant message carrying a tool_call left behind (DeepSeek-safe).
func TestPruneCollapsesToolTurnIntoSnapshots(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/a.go"}`}},
			},
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_2", Name: "search_replace", Arguments: `{"path":"/tmp/a.go","search":"old","replace":"new"}`}},
			},
			{
				Content:    "done",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
				Reasoning:  "reasoning that must be pruned",
			},
		},
	}
	st := &state{provider: provider, prune: true}
	// Seed a prior turn (a plain user->assistant pair) that must be preserved.
	st.history = []llm.Message{
		{Role: llm.RoleUser, Content: "earlier"},
		{Role: llm.RoleAssistant, Content: "previous answer"},
	}
	var w bytes.Buffer

	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle after read tool call")
	}
	// deliver read result
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"file body"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle after search_replace tool call")
	}
	// deliver search_replace result
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"call_2","result":"ok"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if st.inToolCycle {
		t.Fatal("expected tool cycle to finish")
	}

	// history is now [earlier, previous answer, hi(user), <State>, <Replace>, done]
	if len(st.history) != 6 {
		t.Fatalf("history len = %d, want 6 (2 prior + prompt + State + Replace + answer), got %+v", len(st.history), st.history)
	}
	// prior prefix preserved byte-identically
	if st.history[0].Content != "earlier" || st.history[1].Content != "previous answer" {
		t.Fatalf("prior turns should be preserved untouched, got %+v", st.history[:2])
	}
	// user prompt
	if st.history[2].Role != llm.RoleUser || st.history[2].Content != "hi" {
		t.Fatalf("history[2] should be the user prompt, got %+v", st.history[2])
	}
	// <State> snapshot for the read (content from the tool_result)
	stateMsg := st.history[3]
	if stateMsg.Role != llm.RoleUser || !strings.HasPrefix(stateMsg.Content, "<State /tmp/a.go ") || !strings.HasSuffix(stateMsg.Content, ">\"file body\"</State>") {
		t.Fatalf("history[3] should be a <State> snapshot, got %+v", stateMsg)
	}
	// <Replace> delta for the search_replace
	replaceMsg := st.history[4]
	if replaceMsg.Role != llm.RoleUser || !strings.HasPrefix(replaceMsg.Content, "<Replace /tmp/a.go ") || !strings.Contains(replaceMsg.Content, "search:\nold\n---\nreplace:\nnew") || !strings.HasSuffix(replaceMsg.Content, "</Replace>") {
		t.Fatalf("history[4] should be a <Replace> delta, got %+v", replaceMsg)
	}
	// final answer
	last := st.history[5]
	if last.Role != llm.RoleAssistant || last.Content != "done" {
		t.Fatalf("history[5] should be the final assistant answer, got %+v", last)
	}
	// no assistant message with tool_calls, no tool results, no reasoning
	for _, m := range st.history {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("pruned history should contain no tool_calls, got %+v", m)
		}
		if m.Role == "tool" {
			t.Fatalf("pruned history should contain no tool results, got %+v", m)
		}
		if m.Reasoning != "" {
			t.Fatalf("pruned history should contain no reasoning, got %+v", m)
		}
	}
}

// TestPruneReductionKeepsBaselinePlusLaterReplaces covers the path reduction rule
// for the sequence read->replace->read->replace: the baseline is the LAST read, so
// only it plus the replace after it survive (the first read and first replace are
// dropped as redundant). See the `-prune` reduction table.
func TestPruneReductionKeepsBaselinePlusLaterReplaces(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "r1", Name: "read", Arguments: `{"path":"/tmp/a.go"}`}},
			},
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "s1", Name: "search_replace", Arguments: `{"path":"/tmp/a.go","search":"a","replace":"b"}`}},
			},
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "r2", Name: "read", Arguments: `{"path":"/tmp/a.go"}`}},
			},
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "s2", Name: "search_replace", Arguments: `{"path":"/tmp/a.go","search":"c","replace":"d"}`}},
			},
			{
				Content:    "done",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
			},
		},
	}
	st := &state{provider: provider, prune: true}
	var w bytes.Buffer

	_, quit := handleLine(`{"method":"prompt","params":{"text":"edit file"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	for _, id := range []string{"r1", "s1", "r2", "s2"} {
		if !st.inToolCycle {
			t.Fatalf("expected in tool cycle before delivering %s", id)
		}
		_, quit = handleLine(`{"method":"tool_result","params":{"id":"`+id+`","result":"body-`+id+`"}}`, st, &w)
		if quit {
			t.Fatalf("tool_result %s should not quit", id)
		}
	}
	if st.inToolCycle {
		t.Fatal("expected tool cycle to finish")
	}

	// history is now [prompt, <State> (from r2), <Replace> (from s2), done]
	// — the first read/replace pair (r1/s1) is dropped as redundant.
	if len(st.history) != 4 {
		t.Fatalf("history len = %d, want 4 (prompt + baseline State + one Replace + answer), got %+v", len(st.history), st.history)
	}
	if st.history[0].Role != llm.RoleUser || st.history[0].Content != "edit file" {
		t.Fatalf("history[0] should be the user prompt, got %+v", st.history[0])
	}
	stateMsg := st.history[1]
	if stateMsg.Role != llm.RoleUser || !strings.Contains(stateMsg.Content, ">\"body-r2\"</State>") {
		t.Fatalf("history[1] should be the baseline <State> from the 2nd read, got %+v", stateMsg)
	}
	replaceMsg := st.history[2]
	if replaceMsg.Role != llm.RoleUser || !strings.Contains(replaceMsg.Content, "search:\nc\n---\nreplace:\nd") {
		t.Fatalf("history[2] should be the <Replace> from s2 only, got %+v", replaceMsg)
	}
	if st.history[3].Role != llm.RoleAssistant || st.history[3].Content != "done" {
		t.Fatalf("history[3] should be the final answer, got %+v", st.history[3])
	}
}

// TestPruneDropsNonFileTools verifies that non-file tools (grep/shell/glob/code)
// leave no trace in the pruned history: only file read/write/replace produce
// snapshots. A shell tool result is dropped entirely.
func TestPruneDropsNonFileTools(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls: []llm.ToolCall{
					{ID: "sh1", Name: "shell", Arguments: `{"command":"ls"}`},
					{ID: "r1", Name: "read", Arguments: `{"path":"/tmp/b.go"}`},
				},
			},
			{
				Content:    "done",
				StopReason: "END_TURN",
				Usage:      &llm.Usage{},
			},
		},
	}
	st := &state{provider: provider, prune: true}
	var w bytes.Buffer

	_, quit := handleLine(`{"method":"prompt","params":{"text":"explore"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if !st.inToolCycle {
		t.Fatal("expected to be in tool cycle")
	}
	// both tools go to pending; deliver them in any order
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"sh1","result":"ls output"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"r1","result":"b body"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if st.inToolCycle {
		t.Fatal("expected tool cycle to finish")
	}

	// history is now [prompt, <State /tmp/b.go>, done] — the shell result is gone.
	if len(st.history) != 3 {
		t.Fatalf("history len = %d, want 3 (prompt + State + answer), got %+v", len(st.history), st.history)
	}
	if st.history[0].Role != llm.RoleUser || st.history[0].Content != "explore" {
		t.Fatalf("history[0] should be the user prompt, got %+v", st.history[0])
	}
	stateMsg := st.history[1]
	if stateMsg.Role != llm.RoleUser || !strings.Contains(stateMsg.Content, ">\"b body\"</State>") {
		t.Fatalf("history[1] should be the <State> from the read, got %+v", stateMsg)
	}
	if st.history[2].Role != llm.RoleAssistant || st.history[2].Content != "done" {
		t.Fatalf("history[2] should be the final answer, got %+v", st.history[2])
	}
}

// TestPruneKeepsConversationTurns verifies that -prune does NOT touch plain
// conversational turns (no tools): the user prompt and assistant answer are kept
// as-is, nothing reordered or dropped.
func TestPruneKeepsConversationTurns(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "Hello", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp, prune: true}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	if len(st.history) != 2 {
		t.Fatalf("history len = %d, want 2 (prompt + answer)", len(st.history))
	}
	if st.history[0].Role != llm.RoleUser || st.history[0].Content != "hi" {
		t.Fatalf("history[0] should be the user prompt, got %+v", st.history[0])
	}
	if st.history[1].Role != llm.RoleAssistant || st.history[1].Content != "Hello" {
		t.Fatalf("history[1] should be the assistant answer, got %+v", st.history[1])
	}
}

// TestPruneDefaultOff verifies that WITHOUT the -prune flag the interleaved tool
// history (assistant tool_calls + tool results) is preserved across the turn,
// matching the default behavior.
func TestPruneDefaultOff(t *testing.T) {
	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
			},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{provider: provider} // prune defaults to false
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	_, quit = handleLine(`{"method":"tool_result","params":{"id":"call_1","result":"file content"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	// [user, assistant(tool_calls), tool, assistant(final)] — nothing collapsed.
	if len(st.history) != 4 {
		t.Fatalf("history len = %d, want 4 (interleaved, prune off), got %+v", len(st.history), st.history)
	}
	if len(st.history[1].ToolCalls) != 1 || st.history[2].Role != "tool" {
		t.Fatalf("interleaved tool history should be preserved without prune, got %+v", st.history)
	}
}

// ---------------------------------------------------------------------------
// Late / orphaned tool_result hardening (cancel race)
// ---------------------------------------------------------------------------

// TestHandleLineToolResultUnknownIDIgnored verifies that a tool_result whose id
// is NOT among the pending tool calls is silently ignored instead of being
// appended. A late result (e.g. from a command the client kept running after a
// cancel) that lands while a newer turn is in its tool cycle would otherwise
// create an orphaned "tool" message the provider rejects.
func TestHandleLineToolResultUnknownIDIgnored(t *testing.T) {
	st := &state{
		inToolCycle:        true,
		pendingToolIDs:     []string{"call_1"},
		pendingToolResults: map[string]json.RawMessage{},
	}
	var w bytes.Buffer
	resp, quit := handleLine(`{"method":"tool_result","params":{"id":"call_2","result":"late"}}`, st, &w)
	if quit {
		t.Fatal("tool_result should not quit")
	}
	if resp != "" {
		t.Fatalf("unknown-id tool_result should be ignored, got %q", resp)
	}
	if w.Len() != 0 {
		t.Fatalf("expected no output for ignored tool_result, got %q", w.String())
	}
	if len(st.history) != 0 {
		t.Fatalf("unknown-id tool_result must not be appended, got %+v", st.history)
	}
	if !st.inToolCycle || !st.hasPendingToolID("call_1") {
		t.Fatal("still awaiting the real pending tool call")
	}
}

// cancelRaceProvider blocks in Chat until `proceed` is closed, then returns a
// successful response carrying tool_calls — simulating a stream that completes
// at the very moment the user cancels.
type cancelRaceProvider struct {
	started chan struct{}
	proceed chan struct{}
	once    sync.Once
}

func (p *cancelRaceProvider) Chat(_ context.Context, _ llm.ChatRequest, onChunk func(string)) (*llm.ChatResponse, error) {
	p.once.Do(func() { close(p.started) })
	<-p.proceed
	if onChunk != nil {
		onChunk("partial answer")
	}
	return &llm.ChatResponse{
		Content:    "full answer",
		StopReason: "tool_calls",
		Usage:      &llm.Usage{},
		ToolCalls:  []llm.ToolCall{{ID: "call_1", Name: "read", Arguments: `{"path":"/tmp/x"}`}},
	}, nil
}

func (p *cancelRaceProvider) Name() string  { return "race" }
func (p *cancelRaceProvider) Model() string { return "race" }

// TestCancelRaceDuringChatStripsToolCalls verifies the fix for the race where a
// cancel arrives while Chat is finishing: the response must NOT be committed as
// an assistant tool_calls message (which would be an orphan the provider
// rejects), even though Chat returned successfully after the cancel.
func TestCancelRaceDuringChatStripsToolCalls(t *testing.T) {
	provider := &cancelRaceProvider{started: make(chan struct{}), proceed: make(chan struct{})}
	st := &state{provider: provider}
	var w bytes.Buffer

	promptDone := make(chan struct{})
	go func() {
		defer close(promptDone)
		if _, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w); quit {
			t.Error("prompt should not quit")
		}
	}()

	// wait until the provider's Chat has started (and is blocked)
	select {
	case <-provider.started:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt never started Chat")
	}

	// cancel while Chat is still running, then let it return successfully
	resp, quit := handleLine(`{"method":"cancel"}`, st, &w)
	if quit {
		t.Fatal("cancel should not quit")
	}
	if resp != `{"event":"cancelled"}` {
		t.Fatalf("expected cancelled, got %q", resp)
	}
	close(provider.proceed)

	select {
	case <-promptDone:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt handleLine never returned after cancel")
	}

	if st.inToolCycle {
		t.Fatal("cancelled turn must not be left in a tool cycle")
	}
	for _, m := range st.history {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("cancelled turn left orphaned tool_calls in history: %+v", m)
		}
	}
}

// TestRunToolCycleSanitizesOrphanBeforeSend verifies the belt-and-suspenders
// guard in runToolCycle: an orphaned assistant tool_calls message already sitting
// in the history is stripped right before the request is snapshotted, so it can
// never reach the provider.
func TestRunToolCycleSanitizesOrphanBeforeSend(t *testing.T) {
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	// simulate the residue the race could leave behind: an assistant tool_calls
	// message with no corresponding tool result.
	st.history = []llm.Message{
		{Role: llm.RoleUser, Content: "earlier"},
		{Role: llm.RoleAssistant, Content: "", ToolCalls: []llm.ToolCall{{ID: "orphan", Name: "read", Arguments: `{}`}}},
	}
	var w bytes.Buffer
	_, quit := handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if quit {
		t.Fatal("prompt should not quit")
	}
	for _, m := range fp.lastReq.Messages {
		if len(m.ToolCalls) > 0 {
			t.Fatalf("orphaned tool_calls must be sanitized before sending, got %+v", m)
		}
	}
}
