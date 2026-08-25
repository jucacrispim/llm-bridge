package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	appcontext "llm-bridge/context"
	"llm-bridge/history"
	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/protocol"
	"llm-bridge/tools"
)

// defaultSystemPrompt guides the model's behavior. The bridge always exposes
// tools to the model, and agentic models (notably Gemini) will happily call a
// tool even for a plain greeting ("oi" → "git status"). The system prompt
// makes explicit that tools are only for actually inspecting/modifying the
// project or running a command, and that casual conversation must be answered
// with plain text. It is sent fresh on every request (not persisted in the
// history).
const defaultSystemPrompt = `You are a helpful coding assistant working in the user's terminal on their software projects. You have access to tools to read, write, search and replace files, and run shell commands.

Use tools ONLY when the user's request requires inspecting or modifying the project, or running a command. For casual conversation, greetings, or general questions that do not need the project's files, respond with plain text and do NOT call a tool. When you do use a tool, prefer the smallest, most targeted action and run only what the user asked for.`

type state struct {
	cwd            string
	knowledgeBases []json.RawMessage
	// provider is the single fixed provider used when no registry is set
	// (kept for the runWithProvider/Run entry points and existing tests).
	provider llm.LLMProvider
	// providers is an optional registry of named providers that can be
	// switched per request via the prompt's `provider` field. When non-empty it
	// takes precedence over `provider`.
	providers map[string]llm.LLMProvider
	// providerName is the currently active provider in the registry.
	providerName string

	pendingToolIDs     []string
	pendingToolResults map[string]json.RawMessage
	history            []llm.Message
	inToolCycle        bool
	firstTurn          bool

	modelOverride           string
	thinkingOverride        *bool
	reasoningEffortOverride *string

	cancel               context.CancelFunc
	// cancelled reports whether the current turn was cancelled (as opposed to
	// having completed). It is set by cancelTurn and cleared when a new prompt
	// starts. A tool_result that arrives for an already-cancelled turn is
	// silently ignored instead of erroring with "tool_result without pending
	// tool call", because some clients queue a tool_result right before their
	// own cancel (e.g. when a user denies a tool), and the reader goroutine may
	// process the cancel before the main loop gets to the queued tool_result.
	cancelled bool

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

// activeProvider returns the provider to use for the current request. When a
// provider registry is set it resolves the active provider by name (falling back
// to any registered provider if the name is missing); otherwise it returns the
// single fixed provider.
func (st *state) activeProvider() llm.LLMProvider {
	if len(st.providers) > 0 {
		if p, ok := st.providers[st.providerName]; ok {
			return p
		}
		for _, p := range st.providers {
			return p
		}
	}
	return st.provider
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

// setCancelled records whether the current turn was cancelled. Guarded by
// cancelMut because the reader goroutine (cancel handler) writes it while the
// main loop (tool_result handler) may read it.
func (st *state) setCancelled(v bool) {
	st.cancelMut.Lock()
	st.cancelled = v
	st.cancelMut.Unlock()
}

// isCancelled reports whether the current turn was cancelled.
func (st *state) isCancelled() bool {
	st.cancelMut.Lock()
	defer st.cancelMut.Unlock()
	return st.cancelled
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
	st.setCancelled(true)
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
		if st.activeProvider() == nil {
			return string(protocol.NewError("no LLM provider configured")), false
		}
		var p protocol.PromptParams
		if err := json.Unmarshal(cmd.Params, &p); err != nil {
			return string(protocol.NewError("invalid prompt params: " + err.Error())), false
		}

		if st.inToolCycle {
			return string(protocol.NewError("cannot send new prompt while awaiting tool results")), false
		}
		// A new prompt starts a fresh turn: reset the cancelled flag so a
		// stale tool_result from a previous cancelled turn is not swallowed.
		st.setCancelled(false)
		if st.firstTurn {
			entries, _ := appcontext.Load(st.cwd)
			for _, e := range entries {
				st.appendUser(e.Render())
			}
			st.firstTurn = false
		}
		if p.Provider != "" {
			if len(st.providers) == 0 {
				return string(protocol.NewError("provider switching not available: " + p.Provider)), false
			}
			if _, ok := st.providers[p.Provider]; !ok {
				return string(protocol.NewError("unknown provider: " + p.Provider)), false
			}
			st.providerName = p.Provider
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
			// A tool_result that arrives after the turn was cancelled is
			// silently ignored: some clients (e.g. when the user denies a tool)
			// queue a tool_result right before their own cancel, and the reader
			// goroutine may have already processed the cancel by the time the
			// main loop reaches this tool_result. Erroring here is confusing
			// and useless, since the cancelled turn is not going to continue.
			if st.isCancelled() {
				return "", false
			}
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
		resp, err := st.activeProvider().Chat(ctx, llm.ChatRequest{
			Messages:        st.historySnapshot(),
			Tools:           tools.All(),
			System:          defaultSystemPrompt,
			Model:           st.modelOverride,
			Thinking:        st.thinkingOverride,
			ReasoningEffort: st.reasoningEffortOverride,
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
				model = st.activeProvider().Model()
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

// providerNames returns the sorted keys of the provider registry for logging.
func providerNames(providers map[string]llm.LLMProvider) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// run starts the server loop. It accepts either a single fixed provider (via
// runWithProvider) or a named provider registry that can be switched per request
// (via RunWithProviders). When providers is non-empty it takes precedence over
// the single provider.
func run(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string) error {
	if len(providers) > 0 {
		if defaultName == "" {
			for name := range providers {
				defaultName = name
				break
			}
		}
		logger.Infof("starting bridge with providers %v (default %s)", providerNames(providers), defaultName)
	}
	st := &state{providers: providers, providerName: defaultName, firstTurn: true}
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
			if err := json.Unmarshal([]byte(line), &cmd); err == nil &&
				cmd.Method == string(protocol.MethodCancel) {
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

// runWithProvider starts the server loop with a single fixed provider.
func runWithProvider(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	if provider != nil {
		providers := map[string]llm.LLMProvider{provider.Name(): provider}
		return run(r, w, providers, provider.Name())
	}
	return run(r, w, nil, "")
}

// Run starts the server loop reading from r and writing to w.
func Run(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	// notest
	return runWithProvider(r, w, provider)
}

// RunWithProviders starts the server loop with a registry of named providers
// that can be switched per request via the prompt's `provider` field. defaultName
// selects the initial active provider.
func RunWithProviders(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string) error {
	// notest
	return run(r, w, providers, defaultName)
}
