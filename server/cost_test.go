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
	"math"
	"testing"

	"llm-bridge/llm"
)

func costOf(t *testing.T, evt map[string]any, key string) *float64 {
	t.Helper()
	v, ok := evt[key]
	if !ok {
		t.Fatalf("turn_end has no %s", key)
	}
	if v == nil {
		return nil
	}
	f := v.(float64)
	return &f
}

func pricedResponse(c float64) *llm.ChatResponse {
	return &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{CostUSD: &c}}
}

// TestTurnCostAndSession checks the session total grows with each turn.
func TestTurnCostAndSession(t *testing.T) {
	fp := &fakeProvider{name: "fake", resp: pricedResponse(0.25)}
	st := &state{firstTurn: true, provider: fp}

	for i, wantSession := range []float64{0.25, 0.5} {
		var w bytes.Buffer
		handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
		evt := turnEndFrom(t, w.String())
		turn, session := costOf(t, evt, "cost_usd"), costOf(t, evt, "session_cost_usd")
		if turn == nil || math.Abs(*turn-0.25) > 1e-9 {
			t.Errorf("turn %d: cost_usd = %v, want 0.25", i, turn)
		}
		if session == nil || math.Abs(*session-wantSession) > 1e-9 {
			t.Errorf("turn %d: session_cost_usd = %v, want %v", i, session, wantSession)
		}
	}
}

// TestUnpricedTurnReportsNull checks a call with no price hides both totals
// for the turn, and the next priced turn is clean again.
func TestUnpricedTurnReportsNull(t *testing.T) {
	fp := &fakeProvider{name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}}}
	st := &state{firstTurn: true, provider: fp}

	var w bytes.Buffer
	handleLine(`{"method":"prompt","params":{"text":"a"}}`, st, &w)
	evt := turnEndFrom(t, w.String())
	if costOf(t, evt, "cost_usd") != nil || costOf(t, evt, "session_cost_usd") != nil {
		t.Errorf("unpriced turn reported a cost: %v", evt)
	}

	fp.resp = pricedResponse(0.5)
	w.Reset()
	handleLine(`{"method":"prompt","params":{"text":"b"}}`, st, &w)
	evt = turnEndFrom(t, w.String())
	if c := costOf(t, evt, "cost_usd"); c == nil || math.Abs(*c-0.5) > 1e-9 {
		t.Errorf("cost_usd = %v, want 0.5", c)
	}
}

func TestAddCostNilUsage(t *testing.T) {
	st := &state{}
	st.addCost(nil)
	if !st.turnUnpriced || st.sessionCost != 0 {
		t.Errorf("nil usage: unpriced=%v session=%v", st.turnUnpriced, st.sessionCost)
	}
}
