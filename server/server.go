package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	appcontext "llm-bridge/context"
	"llm-bridge/history"
	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/protocol"
	"llm-bridge/tools"
)

type state struct {
	cwd            string
	knowledgeBases []json.RawMessage
	provider       llm.LLMProvider

	pendingToolIDs     []string
	pendingToolResults map[string]json.RawMessage
	history            []llm.Message
	inToolCycle        bool
	firstTurn          bool

	modelOverride         string
	thinkingOverride      *bool
	reasoningEffortOverride *string

	cancel               context.CancelFunc
	historyLenBeforeTurn int

	totalTokens           int
	totalPromptTokens     int
	totalCompletionTokens int

	// cancelMut guards the `cancel` field. It is read/written concurrently by
	// the reader goroutine (which handles `cancel` immediately) and by
	// runToolCycle while a Chat is streaming.
	cancelMut sync.Mutex
	// writeMut serializes all writes to the bridge's stdout so that the reader
	// goroutine's `cancelled` event never interleaves with the chunks the
	// provider's streaming callbacks are writing.
	writeMut sync.Mutex
	// historyMut guards the `history` slice. It is accessed concurrently by the
	// reader goroutine (cancelTurn → history.Sanitize) and by runToolCycle
	// (AppendAssistant once the cancelled Chat returns), so every read/write of
	// the shared history must go through the locked helpers below.
	historyMut sync.Mutex
}

// appendUser appends a user message to the shared conversation history.
func (st *state) appendUser(content string) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendUser(&st.history, content)
}

// appendAssistant appends an assistant message to the shared conversation
// history, preserving any partial thinking/content collected from a stream.
func (st *state) appendAssistant(resp *llm.ChatResponse) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendAssistant(&st.history, resp)
}

// appendToolResult appends a tool response to the shared conversation history.
func (st *state) appendToolResult(id, content string) {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.AppendToolResult(&st.history, id, content)
}

// sanitizeHistory strips ephemeral blocks and orphaned tool_calls messages.
func (st *state) sanitizeHistory() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	history.Sanitize(&st.history)
}

// historySnapshot returns a copy of the conversation history, safe to hand to
// a provider while the reader goroutine may concurrently sanitize it.
func (st *state) historySnapshot() []llm.Message {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	out := make([]llm.Message, len(st.history))
	copy(out, st.history)
	return out
}

// historyLen returns the current length of the shared conversation history.
func (st *state) historyLen() int {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()
	return len(st.history)
}

// write serializes a write to w (the bridge's stdout). All event output goes
// through here so that a `cancelled` event written by the reader goroutine
// cannot interleave with streaming chunk/thinking events.
func (st *state) write(w io.Writer, s string) error {
	st.writeMut.Lock()
	defer st.writeMut.Unlock()
	_, err := fmt.Fprint(w, s)
	return err
}

// setCancel stores the active Chat cancel func (nil when no Chat is running).
func (st *state) setCancel(c context.CancelFunc) {
	st.cancelMut.Lock()
	st.cancel = c
	st.cancelMut.Unlock()
}

// cancelActive invokes and clears the active Chat cancel func, if any.
func (st *state) cancelActive() {
	st.cancelMut.Lock()
	if st.cancel != nil {
		st.cancel()
		st.cancel = nil
	}
	st.cancelMut.Unlock()
}

// cancelTurn interrupts the in-flight turn and resets its state, without
// emitting any event. It is the shared core of both the `cancel` command
// handler and the reader goroutine's immediate-cancel path.
//
// Unlike a rollback, cancellation deliberately KEEPS the turn's history: the
// user's prompt (appended when the turn started) and any partial thinking /
// content the model already produced (persisted to the history by
// runToolCycle once the streaming Chat returns with context.Canceled) stay in
// the conversation so the user keeps the context of what was asked and what
// was in progress.
func (st *state) cancelTurn() {
	st.cancelActive()
	st.inToolCycle = false
	st.pendingToolIDs = nil
	st.pendingToolResults = nil
	// Drop any orphaned assistant message with tool_calls that never received
	// a tool result. Cancelling mid-tool-cycle leaves such a message in the
	// history (runToolCycle appended it before waiting for the results); if it
	// were kept, the next prompt would send it to the API without the required
	// corresponding "tool" messages (an assistant tool_calls message must be
	// followed by tool responses for each tool_call_id), which the provider
	// rejects with HTTP 400. Sanitize strips those orphans while preserving the
	// user prompt and any partial assistant content, keeping the cancelled
	// turn's context.
	st.sanitizeHistory()
}

type setCwdParams struct {
	Cwd string `json:"cwd"`
}

type setKnowledgeBasesParams struct {
	Bases []json.RawMessage `json:"bases"`
}

type toolResultParams struct {
	ID     string          `json:"id"`
	Result json.RawMessage `json:"result"`
}

func handleLine(line string, st *state, w io.Writer) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false
	}

	var cmd protocol.InboundCommand
	if err := json.Unmarshal([]byte(line), &cmd); err != nil {
		return string(protocol.NewError("invalid JSON: " + err.Error())), false
	}

	switch cmd.Method {
	case string(protocol.MethodPrompt):
		if st.provider == nil {
			return string(protocol.NewError("no LLM provider configured")), false
		}
		var p protocol.PromptParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid prompt params: " + err.Error())), false
		}

		if st.inToolCycle {
			return string(protocol.NewError("cannot send new prompt while awaiting tool results")), false
		}
		if st.firstTurn {
			entries, _ := appcontext.Load(st.cwd)
			for _, e := range entries {
				st.appendUser(e.Render())
			}
			st.firstTurn = false
		}
		if p.Model != "" {
			st.modelOverride = p.Model
		}
		if p.Thinking != nil {
			st.thinkingOverride = p.Thinking
		}
		if p.ReasoningEffort != nil {
			st.reasoningEffortOverride = p.ReasoningEffort
		}
		st.historyLenBeforeTurn = st.historyLen() // context preserved on cancel
		st.appendUser(p.Text)
		return runToolCycle(st, w)

	case string(protocol.MethodCancel):
		st.cancelTurn()
		return string(protocol.NewCancelled()), false

	case string(protocol.MethodSetCwd):
		var p setCwdParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid set_cwd params: " + err.Error())), false
		}
		st.cwd = p.Cwd
		return "", false

	case string(protocol.MethodSetKnowledgeBases):
		var p setKnowledgeBasesParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid set_knowledge_bases params: " + err.Error())), false
		}
		st.knowledgeBases = p.Bases
		return "", false

	case string(protocol.MethodToolResult):
		if !st.inToolCycle || len(st.pendingToolIDs) == 0 {
			return string(protocol.NewError("tool_result without pending tool call")), false
		}
		var p toolResultParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid tool_result params: " + err.Error())), false
		}
		if _, ok := st.pendingToolResults[p.ID]; ok {
			return string(protocol.NewError("duplicate tool_result for id " + p.ID)), false
		}
		st.pendingToolResults[p.ID] = p.Result
		st.appendToolResult(p.ID, string(p.Result))
		allDone := true
		for _, id := range st.pendingToolIDs {
			if _, ok := st.pendingToolResults[id]; !ok {
				allDone = false
				break
			}
		}
		if !allDone {
			return "", false
		}
		st.pendingToolIDs = nil
		st.pendingToolResults = nil
		st.inToolCycle = false
		return runToolCycle(st, w)

	case string(protocol.MethodQuit):
		return "", true

	default:
		return string(protocol.NewError("unknown method " + cmd.Method)), false
	}
}

func runToolCycle(st *state, w io.Writer) (string, bool) {
	for {
		ctx, cancel := context.WithCancel(context.Background())
		// Publish the cancel func so the reader goroutine can interrupt this
		// Chat immediately when a `cancel` command arrives mid-stream.
		st.setCancel(cancel)
		emittedChunk := false
		emittedReasoning := false
		// Accumulate the streamed thinking/content locally so that, if the
		// turn is cancelled mid-stream, the partial output is still preserved
		// in the history below instead of being lost.
		var partialContent strings.Builder
		var partialReasoning strings.Builder
		resp, err := st.provider.Chat(ctx, llm.ChatRequest{
			Messages: st.historySnapshot(),
			Tools:    tools.All(),
			Model:            st.modelOverride,
			Thinking:         st.thinkingOverride,
			ReasoningEffort:  st.reasoningEffortOverride,
			// The chain-of-thought streams before the content; surface it to the
			// client as thinking events so it can display the reasoning.
			OnReasoning: func(s string) {
				partialReasoning.WriteString(s)
				emittedReasoning = true
				_ = st.write(w, string(protocol.NewThinking(s))+"\n")
			},
		}, func(s string) {
			partialContent.WriteString(s)
			emittedChunk = true
			_ = st.write(w, string(protocol.NewChunk(s))+"\n")
		})
		st.setCancel(nil)
		if err != nil {
			cancel()
			if errors.Is(err, context.Canceled) {
				// The turn was cancelled. The user's prompt is already in the
				// history (cancelTurn no longer rolls it back); persist whatever
				// thinking/content the model produced before being interrupted so
				// the cancelled turn's context is kept for the next prompt.
				if partialContent.Len() > 0 || partialReasoning.Len() > 0 {
					st.appendAssistant(&llm.ChatResponse{
						Content:   partialContent.String(),
						Reasoning: partialReasoning.String(),
					})
				}
				return "", false
			}
			logger.Errorf("provider error: %v", err)
			return string(protocol.NewError(err.Error())), false
		}
		cancel()

		inputTokens := 0
		outputTokens := 0
		if resp.Usage != nil {
			inputTokens = resp.Usage.PromptTokens
			outputTokens = resp.Usage.CompletionTokens
		}
		totalTokensThisTurn := inputTokens + outputTokens
		st.totalTokens += totalTokensThisTurn
		st.totalPromptTokens += inputTokens
		st.totalCompletionTokens += outputTokens

		if !emittedChunk && resp.Content != "" {
			_ = st.write(w, string(protocol.NewChunk(resp.Content))+"\n")
		}
		// Providers that return the reasoning without streaming it still get the
		// thinking surfaced to the client, as a single event.
		if !emittedReasoning && resp.Reasoning != "" {
			_ = st.write(w, string(protocol.NewThinking(resp.Reasoning))+"\n")
		}

		st.appendAssistant(resp)

		for _, tc := range resp.ToolCalls {
			var input any = map[string]any{}
			if tc.Arguments != "" {
				input = json.RawMessage(tc.Arguments)
			}
			evt, err := protocol.NewToolCall(tc.ID, tc.Name, input)
			if err == nil {
				_ = st.write(w, string(evt)+"\n")
			}
		}

		if len(resp.ToolCalls) == 0 {
			contextPct := 0.0
			model := resp.Model
			if model == "" {
				model = st.provider.Model()
			}
			newEnd := protocol.NewTurnEnd(resp.StopReason, &contextPct,
				model, inputTokens, outputTokens, totalTokensThisTurn)
			_ = st.write(w, string(newEnd)+"\n")

			newUsage := protocol.NewUsageDelta(inputTokens, outputTokens, totalTokensThisTurn)
			_ = st.write(w, string(newUsage)+"\n")
			st.sanitizeHistory()
			st.inToolCycle = false
			st.pendingToolIDs = nil
			st.pendingToolResults = nil
			st.historyLenBeforeTurn = 0
			return "", false
		}

		st.pendingToolIDs = make([]string, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			st.pendingToolIDs = append(st.pendingToolIDs, tc.ID)
		}
		st.pendingToolResults = make(map[string]json.RawMessage)
		st.inToolCycle = true
		return "", false
	}
}

// inboundLine carries either a raw command line or the reader's terminal error.
type inboundLine struct {
	line string
	err  error
}

func runWithProvider(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	if provider != nil {
		logger.Infof("starting bridge with provider %s", provider.Name())
	}
	st := &state{provider: provider, firstTurn: true}
	_ = st.write(w, string(protocol.NewReady())+"\n")
	logger.Debugf("ready sent")

	// A reader goroutine keeps consuming stdin while the main loop is blocked
	// inside a streaming Chat. A `cancel` command is handled right here, so it
	// interrupts the in-flight request immediately instead of waiting for the
	// model to stop talking (the previous synchronous loop could not even read
	// the cancel until Chat returned). All other commands are forwarded to the
	// main loop, which processes them serially.
	cmdCh := make(chan inboundLine, 16)
	go func() {
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			logger.Debugf("received: %s", line)
			var cmd protocol.InboundCommand
			if err := json.Unmarshal([]byte(line), &cmd); err == nil && cmd.Method == string(protocol.MethodCancel) {
				// Cancel in flight: stop the streaming request and reply at once.
				st.cancelTurn()
				_ = st.write(w, string(protocol.NewCancelled())+"\n")
				continue
			}
			cmdCh <- inboundLine{line: line}
		}
		if err := scanner.Err(); err != nil {
			cmdCh <- inboundLine{err: err}
		}
		close(cmdCh)
	}()

	for item := range cmdCh {
		if item.err != nil {
			logger.Errorf("scanner error: %v", item.err)
			return item.err
		}
		resp, quit := handleLine(item.line, st, w)
		if quit {
			logger.Infof("quit requested")
			return nil
		}
		if resp != "" {
			// notest
			logger.Debugf("sending response: %s", resp)
			if err := st.write(w, resp+"\n"); err != nil {
				logger.Errorf("error writing response: %v", err)
				return err
			}
		}
	}
	logger.Infof("bridge finished")
	return nil
}

// Run starts the server loop reading from r and writing to w.
func Run(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	// notest
	return runWithProvider(r, w, provider)
}
