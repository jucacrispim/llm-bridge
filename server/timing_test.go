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

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"llm-bridge/llm"
)

// fakeClock only moves when told. pending is added after the next read, so the
// span between two reads is known.
type fakeClock struct {
	t       time.Time
	pending time.Duration
}

func (c *fakeClock) now() time.Time {
	v := c.t
	c.t = c.t.Add(c.pending)
	c.pending = 0
	return v
}

// slowProvider moves the fake clock forward by chat on every call.
type slowProvider struct {
	*fakeProvider
	clock *fakeClock
	chat  time.Duration
}

func (p *slowProvider) Chat(ctx context.Context, req llm.ChatRequest, onChunk func(string)) (*llm.ChatResponse, error) {
	p.clock.t = p.clock.t.Add(p.chat)
	return p.fakeProvider.Chat(ctx, req, onChunk)
}

func useClock(t *testing.T, c *fakeClock) {
	t.Helper()
	prev := now
	now = c.now
	t.Cleanup(func() { now = prev })
}

func turnEndFrom(t *testing.T, out string) map[string]any {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, `"event":"turn_end"`) {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			t.Fatal(err)
		}
		return evt
	}
	t.Fatalf("no turn_end in %q", out)
	return nil
}

// TestTurnEndTimings runs a turn with two provider calls and one tool, and a
// wait for the approval in between. The wait is in duration_ms and in neither
// of the parts.
func TestTurnEndTimings(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	useClock(t, clock)
	fp := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{StopReason: "tool_calls", Usage: &llm.Usage{}, ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "write", Arguments: `{"path":"x.txt","content":"y"}`},
			}},
			{Content: "done", StopReason: "END_TURN", Usage: &llm.Usage{}},
		},
	}
	st := &state{firstTurn: true, cwd: t.TempDir(),
		provider: &slowProvider{fakeProvider: fp, clock: clock, chat: 700 * time.Millisecond}}
	var w bytes.Buffer

	handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	if !st.inToolCycle {
		t.Fatal("expected to wait for the approval")
	}
	clock.t = clock.t.Add(5 * time.Second)
	clock.pending = 250 * time.Millisecond
	resp, _ := handleLine(`{"method":"tool_confirm","params":{"id":"call_1"}}`, st, &w)
	if resp != "" {
		t.Fatalf("tool_confirm returned %q", resp)
	}

	evt := turnEndFrom(t, w.String())
	want := map[string]float64{"duration_ms": 6650, "llm_ms": 1400, "tool_ms": 250}
	for k, v := range want {
		if got, _ := evt[k].(float64); got != v {
			t.Errorf("%s = %v, want %v", k, evt[k], v)
		}
	}
}

// TestTurnTimingsResetEachPrompt checks a new prompt starts from zero.
func TestTurnTimingsResetEachPrompt(t *testing.T) {
	clock := &fakeClock{t: time.Unix(1000, 0)}
	useClock(t, clock)
	fp := &fakeProvider{name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}}}
	st := &state{firstTurn: true,
		provider: &slowProvider{fakeProvider: fp, clock: clock, chat: 300 * time.Millisecond}}

	for i := 0; i < 2; i++ {
		var w bytes.Buffer
		handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
		evt := turnEndFrom(t, w.String())
		if got, _ := evt["llm_ms"].(float64); got != 300 {
			t.Errorf("turn %d: llm_ms = %v, want 300", i, evt["llm_ms"])
		}
		if got, _ := evt["tool_ms"].(float64); got != 0 {
			t.Errorf("turn %d: tool_ms = %v, want 0", i, evt["tool_ms"])
		}
	}
}
