package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

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

	cancel               context.CancelFunc
	historyLenBeforeTurn int

	totalTokens           int
	totalPromptTokens     int
	totalCompletionTokens int
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
		st.historyLenBeforeTurn = len(st.history) // context preserved on cancel
		history.AppendUser(&st.history, p.Text)
		return runToolCycle(st, w)

	case string(protocol.MethodCancel):
		if st.cancel != nil {
			st.cancel()
			st.cancel = nil
		}
		if st.historyLenBeforeTurn >= 0 && st.historyLenBeforeTurn <= len(st.history) {
			st.history = st.history[:st.historyLenBeforeTurn]
		}
		st.inToolCycle = false
		st.pendingToolIDs = nil
		st.pendingToolResults = nil
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
		st.cancel = cancel
		emittedChunk := false
		resp, err := st.provider.Chat(ctx, llm.ChatRequest{
			Messages: st.history,
			Tools:    tools.All(),
		}, func(s string) {
			emittedChunk = true
			_, _ = fmt.Fprint(w, string(protocol.NewChunk(s))+"\n")
		})
		st.cancel = nil
		if err != nil {
			cancel()
			if errors.Is(err, context.Canceled) {
				// request was cancelled, history already cleared in cancel handler
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
			_, _ = fmt.Fprint(w, string(protocol.NewChunk(resp.Content))+"\n")
		}

		history.AppendAssistant(&st.history, resp)

		for _, tc := range resp.ToolCalls {
			var input any = map[string]any{}
			if tc.Arguments != "" {
				input = json.RawMessage(tc.Arguments)
			}
			evt, err := protocol.NewToolCall(tc.ID, tc.Name, input)
			if err == nil {
				_, _ = fmt.Fprint(w, string(evt)+"\n")
			}
		}

		if len(resp.ToolCalls) == 0 {
			contextPct := 0.0
			newEnd := protocol.NewTurnEnd(resp.StopReason, &contextPct,
				st.provider.Model(), inputTokens, outputTokens, totalTokensThisTurn)
			_, _ = fmt.Fprint(w, string(newEnd)+"\n")

			newUsage := protocol.NewUsageDelta(inputTokens, outputTokens, totalTokensThisTurn)
			_, _ = fmt.Fprint(w, string(newUsage)+"\n")
			history.Sanitize(&st.history)
			st.history = st.history
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

func runWithProvider(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	if provider != nil {
		logger.Infof("starting bridge with provider %s", provider.Name())
	}
	fmt.Fprintln(w, string(protocol.NewReady()))
	logger.Debugf("ready sent")
	st := &state{provider: provider, firstTurn: true}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()
		logger.Debugf("received: %s", line)
		resp, quit := handleLine(line, st, w)
		if quit {
			logger.Infof("quit requested")
			return nil
		}
		if resp != "" {
			// notest
			logger.Debugf("sending response: %s", resp)
			if _, err := fmt.Fprintln(w, resp); err != nil {
				logger.Errorf("error writing response: %v", err)
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		logger.Errorf("scanner error: %v", err)
		return err
	}
	logger.Infof("bridge finished")
	return nil
}

// Run starts the server loop reading from r and writing to w.
func Run(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	// notest
	return runWithProvider(r, w, provider)
}
