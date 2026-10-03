//go:build unix

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
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"llm-bridge/llm"
)

// TestRunToolCycleStopsWhenCancelledBetweenToolCalls covers the in-loop guard of
// runToolCycle (the `if st.isCancelled() { return "", false }` at the top of the
// per-tool-call loop): when a turn is cancelled while a read-only tool is
// running, the remaining tool calls must not be executed and the turn must end.
//
// The first read-only tool reads a FIFO, which blocks until the test opens the
// write end. That gives a deterministic rendezvous: the test only marks the turn
// cancelled once it is certain the turn is inside the tool loop (past the first
// iteration's cancel check), and only then unblocks the read.
func TestRunToolCycleStopsWhenCancelledBetweenToolCalls(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "pipe")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("cannot create fifo: %v", err)
	}

	provider := &fakeProvider{
		name: "fake",
		responses: []*llm.ChatResponse{
			{
				Content:    "",
				StopReason: "tool_calls",
				Usage:      &llm.Usage{},
				ToolCalls: []llm.ToolCall{
					{ID: "c1", Name: "read", Arguments: `{"path":"` + fifo + `"}`},
					{ID: "c2", Name: "glob", Arguments: `{"pattern":"*.txt"}`},
				},
			},
		},
	}
	st := &state{provider: provider}
	var w bytes.Buffer

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = handleLine(`{"method":"prompt","params":{"text":"hi"}}`, st, &w)
	}()

	// Opening the FIFO for writing blocks until the read-only tool opens it for
	// reading, i.e. until the turn is inside executeTool, past the first
	// iteration's cancel check.
	wfCh := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			wfCh <- nil
			return
		}
		wfCh <- f
	}()

	var wf *os.File
	select {
	case wf = <-wfCh:
	case <-time.After(2 * time.Second):
		t.Fatal("tool never opened the fifo")
	}
	if wf == nil {
		t.Fatal("failed to open the fifo for writing")
	}

	// Rendevous reached: mark the turn cancelled, then unblock the read so the
	// tool returns and the loop reaches the next iteration's cancel check.
	st.setCancelled(true)
	_, _ = wf.Write([]byte("data"))
	_ = wf.Close()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("prompt handleLine did not return after cancel")
	}

	// No tool result may have been appended for the cancelled turn.
	for _, m := range st.history {
		if m.Role == "tool" {
			t.Fatalf("cancelled turn appended a tool result: %+v", m)
		}
	}
}
