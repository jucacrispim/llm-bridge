package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"llm-bridge/internal/history"
	"llm-bridge/internal/llm"
	"llm-bridge/internal/protocol"
	"llm-bridge/internal/tools"
)

type state struct {
	cwd            string
	knowledgeBases []json.RawMessage
	provider       llm.LLMProvider

	pendingToolIDs     []string
	pendingToolResults map[string]json.RawMessage
	history            []llm.Message
	inToolCycle        bool

	cancel              context.CancelFunc
	historyLenBeforeTurn int
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
		st.historyLenBeforeTurn = len(st.history)
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

	case string(protocol.MethodStatus):
		return string(protocol.NewStatus(protocol.Usage{}, nil)), false

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
			return string(protocol.NewError(err.Error())), false
		}
		cancel()

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
			metering := map[string]any{"credits": 0.0}
			_, _ = fmt.Fprint(w, string(protocol.NewTurnEnd(resp.StopReason, &contextPct, metering, st.provider.Model()))+"\n")
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
	fmt.Fprintln(w, string(protocol.NewReady()))
	st := &state{provider: provider}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		resp, quit := handleLine(scanner.Text(), st, w)
		if quit {
			return nil
		}
		if resp != "" {
			if _, err := fmt.Fprintln(w, resp); err != nil {
				return err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

// Run starts the server loop reading from r and writing to w.
func Run(r io.Reader, w io.Writer, provider llm.LLMProvider) error {
	// notest
	return runWithProvider(r, w, provider)
}
