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

// cancelTurn rolls back the in-flight turn and resets its state, without
// emitting any event. It is the shared core of both the `cancel` command
// handler and the reader goroutine's immediate-cancel path.
func (st *state) cancelTurn() {
	st.cancelActive()
	if st.historyLenBeforeTurn >= 0 && st.historyLenBeforeTurn <= len(st.history) {
		st.history = st.history[:st.historyLenBeforeTurn]
	}
	st.inToolCycle = false
	st.pendingToolIDs = nil
	st.pendingToolResults = nil
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
				history.AppendUser(&st.history, e.Render())
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
		st.historyLenBeforeTurn = len(st.history) // context preserved on cancel
		history.AppendUser(&st.history, p.Text)
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
		history.AppendToolResult(&st.history, p.ID, string(p.Result))
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
		resp, err := st.provider.Chat(ctx, llm.ChatRequest{
			Messages: st.history,
			Tools:    tools.All(),
			Model:            st.modelOverride,
			Thinking:         st.thinkingOverride,
			ReasoningEffort:  st.reasoningEffortOverride,
			// The chain-of-thought streams before the content; surface it to the
			// client as thinking events so it can display the reasoning.
			OnReasoning: func(s string) {
				emittedReasoning = true
				_ = st.write(w, string(protocol.NewThinking(s))+"\n")
			},
		}, func(s string) {
			emittedChunk = true
			_ = st.write(w, string(protocol.NewChunk(s))+"\n")
		})
		st.setCancel(nil)
		if err != nil {
			cancel()
			if errors.Is(err, context.Canceled) {
				// The request was cancelled; the reader goroutine already rolled
				// back the history and emitted the `cancelled` event.
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

		history.AppendAssistant(&st.history, resp)

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
			history.Sanitize(&st.history)
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
