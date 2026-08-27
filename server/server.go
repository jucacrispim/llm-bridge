package server

import (
	"bufio"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	appcontext "llm-bridge/context"
	"llm-bridge/history"
	"llm-bridge/llm"
	"llm-bridge/logger"
	"llm-bridge/protocol"
	"llm-bridge/tools"
)

//go:embed system_prompt.md
var systemPromptFS embed.FS

var defaultSystemPrompt = func() string {
	b, err := systemPromptFS.ReadFile("system_prompt.md")
	if err != nil {
		panic(fmt.Sprintf("failed to read embedded system_prompt.md: %v", err))
	}
	return strings.TrimSpace(string(b))
}()

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
	systemOverride          string
	systemPrompt            string

	// aggressivePrune, when enabled, collapses each completed tool-calling turn
	// into just the user prompt + the final assistant answer, dropping the
	// intermediate tool_calls, tool results, and their chain-of-thought. Off by
	// default; enabled via the -aggressive-prune flag.
	aggressivePrune bool

	// prune, when enabled, collapses reasoning and tool results of file tools
	// into user snapshots (read/write/replace), preserving file state without
	// storing tool calls or reasoning.
	prune bool

	cancel context.CancelFunc
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

// effectiveSystem returns the system prompt to use for the current request,
// resolving per-request override, global server system prompt, and default.
func (st *state) effectiveSystem() string {
	if st.systemOverride != "" {
		return st.systemOverride
	}
	if st.systemPrompt != "" {
		return st.systemPrompt
	}
	return defaultSystemPrompt
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

// collapseTurn compresses a completed tool-calling turn into just the user
// prompt that opened it plus the final assistant answer, dropping everything in
// between: the intermediate assistant tool_calls messages, the tool results,
// and their chain-of-thought. The prefix (context + prior turns) is kept
// byte-identical, so the provider's context/prefix cache stays reusable across
// turns while the history stays small.
//
// Only turns that actually exercised tools are collapsed (a turn is more than
// prompt + one assistant answer). Conversation-only turns are left untouched.
// Cancelled turns never reach here (collapse runs only on successful
// completion), so their preserved partial context is not affected.
func (st *state) collapseTurn() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()

	start := st.historyLenBeforeTurn
	if start < 0 || start >= len(st.history) {
		return
	}
	last := st.history[len(st.history)-1]
	// No final plain assistant answer → nothing to collapse cleanly.
	if last.Role != llm.RoleAssistant || len(last.ToolCalls) > 0 {
		return
	}
	// A turn with no intermediate tool activity is already prompt + answer.
	if len(st.history)-start <= 2 {
		return
	}

	collapsed := make([]llm.Message, 0, start+2)
	collapsed = append(collapsed, st.history[:start]...)
	collapsed = append(collapsed, st.history[start]) // the user prompt of this turn
	collapsed = append(collapsed, llm.Message{
		Role:    llm.RoleAssistant,
		Content: last.Content,
	})
	st.history = collapsed
}

type fileEvent struct {
	path    string
	kind    string // "state" or "replace"
	content string
	seq     int
}

// pruneTurn collapses reasoning and intermediate tool calls/results into
// structured user snapshots (<State> and <Replace>), preserving file states
// while dropping tool calls, tool results, and chain-of-thought from history.
//
// Unlike collapseTurn which completely discards intermediate tool activity,
// pruneTurn preserves the final state of modified/read files as immutable
// user messages (`<State>` and `<Replace>`), preventing the model from having
// to reread files in subsequent turns while keeping history compact and free
// of heavy reasoning blocks.
func (st *state) pruneTurn() {
	st.historyMut.Lock()
	defer st.historyMut.Unlock()

	// Ensure turn boundaries are valid.
	start := st.historyLenBeforeTurn
	if start < 0 || start >= len(st.history) {
		return
	}
	last := st.history[len(st.history)-1]
	// A valid turn to prune must end with a plain assistant response (no pending tool calls).
	if last.Role != llm.RoleAssistant || len(last.ToolCalls) > 0 {
		return
	}
	// Turns with no intermediate tool activity do not need file pruning.
	if len(st.history)-start <= 2 {
		return
	}

	turnMessages := st.history[start : len(st.history)-1]

	type toolCallInfo struct {
		name string
		args map[string]any
	}
	// Map tool call IDs to their metadata (tool name and parsed arguments).
	calls := make(map[string]toolCallInfo)

	for _, msg := range turnMessages {
		if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				var args map[string]any
				if tc.Arguments != "" {
					_ = json.Unmarshal([]byte(tc.Arguments), &args)
				}
				calls[tc.ID] = toolCallInfo{name: tc.Name, args: args}
			}
		}
	}

	var events []fileEvent
	seqCounter := 0

	// Extract file events (reads, writes, replaces) from tool results and tool arguments.
	for _, msg := range turnMessages {
		if msg.Role == "tool" {
			info, ok := calls[msg.ToolCallID]
			if !ok {
				continue
			}

			switch info.name {
			case "read":
				path, _ := info.args["path"].(string)
				if path != "" {
					events = append(events, fileEvent{
						path:    path,
						kind:    "state",
						content: msg.Content,
						seq:     seqCounter,
					})
					seqCounter++
				}
			case "write":
				path, _ := info.args["path"].(string)
				content, _ := info.args["content"].(string)
				if path != "" {
					events = append(events, fileEvent{
						path:    path,
						kind:    "state",
						content: content,
						seq:     seqCounter,
					})
					seqCounter++
				}
			case "search_replace":
				path, _ := info.args["path"].(string)
				search, _ := info.args["search"].(string)
				replace, _ := info.args["replace"].(string)
				if path != "" {
					repContent := fmt.Sprintf("search:\n%s\n---\nreplace:\n%s", search, replace)
					events = append(events, fileEvent{
						path:    path,
						kind:    "replace",
						content: repContent,
						seq:     seqCounter,
					})
					seqCounter++
				}
			}
		} else if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				info := calls[tc.ID]
				if info.name == "write" {
					path, _ := info.args["path"].(string)
					content, _ := info.args["content"].(string)
					if path != "" {
						events = append(events, fileEvent{
							path:    path,
							kind:    "state",
							content: content,
							seq:     seqCounter,
						})
						seqCounter++
					}
				}
			}
		}
	}

	eventsByPath := make(map[string][]fileEvent)
	for _, ev := range events {
		eventsByPath[ev.path] = append(eventsByPath[ev.path], ev)
	}

	// Apply path-based reduction rule:
	// Baseline = the last state (read/write) snapshot for each path.
	// Keep the baseline plus all replaces occurring after it; drop prior events.
	var survivingEvents []fileEvent
	for _, pathEvents := range eventsByPath {
		lastStateIdx := -1
		for i := len(pathEvents) - 1; i >= 0; i-- {
			if pathEvents[i].kind == "state" {
				lastStateIdx = i
				break
			}
		}

		if lastStateIdx != -1 {
			survivingEvents = append(survivingEvents, pathEvents[lastStateIdx:]...)
		} else {
			survivingEvents = append(survivingEvents, pathEvents...)
		}
	}

	// Restore original chronological order among surviving events across paths.
	sort.Slice(survivingEvents, func(i, j int) bool {
		return survivingEvents[i].seq < survivingEvents[j].seq
	})

	// Rebuild the history for this turn: original user prompt + file snapshots/deltas + final answer.
	var newHistory []llm.Message
	newHistory = append(newHistory, st.history[:start]...)
	newHistory = append(newHistory, st.history[start])

	datetime := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	for _, ev := range survivingEvents {
		var markup string
		if ev.kind == "state" {
			markup = fmt.Sprintf("<State %s %s>%s</State>", ev.path, datetime, ev.content)
		} else {
			markup = fmt.Sprintf("<Replace %s %s>%s</Replace>", ev.path, datetime, ev.content)
		}
		newHistory = append(newHistory, llm.Message{
			Role:    llm.RoleUser,
			Content: markup,
		})
	}

	newHistory = append(newHistory, llm.Message{
		Role:    llm.RoleAssistant,
		Content: last.Content,
	})

	st.history = newHistory
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
		sys := p.System
		if sys == "" {
			sys = p.SystemPrompt
		}
		if sys != "" {
			st.systemOverride = sys
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
			System:          st.effectiveSystem(),
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
			// Aggressive prune: collapse a tool-calling turn into just its user
			// prompt + final answer, keeping the history small and its prefix
			// byte-stable for provider caching. Runs before historyLenBeforeTurn
			// is reset, since it needs the turn's start marker.
			if st.aggressivePrune {
				st.collapseTurn()
			}
			if st.prune {
				st.pruneTurn()
			}
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
func run(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string, systemPrompt string, aggressivePrune bool, prune bool) error {
	if len(providers) > 0 {
		if defaultName == "" {
			for name := range providers {
				defaultName = name
				break
			}
		}
		logger.Infof("starting bridge with providers %v (default %s)", providerNames(providers), defaultName)
	}
	if aggressivePrune {
		logger.Infof("aggressive prune enabled: collapsing tool-calling turns")
	}
	if prune {
		logger.Infof("prune enabled: collapsing reasoning and preserving file snapshots")
	}
	st := &state{providers: providers, providerName: defaultName, systemPrompt: systemPrompt, firstTurn: true, aggressivePrune: aggressivePrune, prune: prune}
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
		// Tool results can be large (the client sends a whole file read, a grep
		// with many matches or a shell with lots of output) as a single JSON
		// line. The default bufio.Scanner limit is 64KB per line; beyond that it
		// returns ErrTooLong and the whole bridge exits with status 1. Raise the
		// limit so a large tool result no longer kills the process.
		scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // até 8MB por linha
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
		return run(r, w, providers, provider.Name(), "", false, false)
	}
	return run(r, w, nil, "", "", false, false)
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
	return RunWithSystemPrompt(r, w, providers, defaultName, "")
}

// RunWithSystemPrompt starts the server loop with a registry of named providers
// and an initial global system prompt.
func RunWithSystemPrompt(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName string, systemPrompt string) error {
	// notest
	return run(r, w, providers, defaultName, systemPrompt, false, false)
}

// RunWithOptions starts the server loop with a registry of named providers, an
// initial global system prompt, and the aggressive-prune option (see state).
func RunWithOptions(r io.Reader, w io.Writer, providers map[string]llm.LLMProvider, defaultName, systemPrompt string, aggressivePrune bool, prune bool) error {
	// notest
	return run(r, w, providers, defaultName, systemPrompt, aggressivePrune, prune)
}
