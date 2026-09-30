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

package protocol

import (
	"strings"
	"testing"
)

func TestParseInboundValid(t *testing.T) {
	cmd, err := ParseInbound([]byte(`{"method":"prompt","params":{"text":"hi"}}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cmd.Method != "prompt" {
		t.Errorf("method = %q, want prompt", cmd.Method)
	}
	if !strings.Contains(string(cmd.Params), `"text":"hi"`) {
		t.Errorf("params = %s, want to contain text hi", cmd.Params)
	}
}

func TestParseInboundInvalid(t *testing.T) {
	if _, err := ParseInbound([]byte(`bad`)); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestNewReady(t *testing.T) {
	got := string(NewReady())
	want := `{"event":"ready"}`
	if got != want {
		t.Errorf("NewReady() = %s, want %s", got, want)
	}
}

func TestNewChunk(t *testing.T) {
	got := string(NewChunk("hi"))
	want := `{"event":"chunk","text":"hi"}`
	if got != want {
		t.Errorf("NewChunk() = %s, want %s", got, want)
	}
}

func TestNewToolCall(t *testing.T) {
	input := map[string]string{"command": "create"}
	gotBytes, err := NewToolCall("t1", "write", input)
	if err != nil {
		t.Fatalf("NewToolCall error: %v", err)
	}
	got := string(gotBytes)
	want := `{"event":"tool_call","id":"t1","name":"write","input":{"command":"create"}}`
	if got != want {
		t.Errorf("NewToolCall() = %s, want %s", got, want)
	}
}

func TestNewToolCallMarshalError(t *testing.T) {
	_, err := NewToolCall("t1", "write", make(chan int))
	if err == nil {
		t.Fatal("expected error for channel input")
	}
}

func TestNewTurnEnd(t *testing.T) {
	ctx := 2.5
	got := string(NewTurnEnd("END_TURN", &ctx, "model-x", 10, 5, 15, 80, 20, 50_000, 1_000_000))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":2.5,"context_tokens":50000,"context_window":1000000,"model":"model-x","input_tokens":10,"output_tokens":5,"total_tokens":15,"cache_hit_tokens":80,"cache_miss_tokens":20}`
	if got != want {
		t.Errorf("NewTurnEnd() = %s, want %s", got, want)
	}
}

func TestNewTurnEndNilContext(t *testing.T) {
	got := string(NewTurnEnd("END_TURN", nil, "", 0, 0, 0, 0, 0, 0, 0))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":null,"context_tokens":0,"context_window":0,"input_tokens":0,"output_tokens":0,"total_tokens":0,"cache_hit_tokens":0,"cache_miss_tokens":0}`
	if got != want {
		t.Errorf("NewTurnEnd(nil) = %s, want %s", got, want)
	}
}

func TestNewError(t *testing.T) {
	got := string(NewError("boom"))
	want := `{"event":"error","message":"boom"}`
	if got != want {
		t.Errorf("NewError() = %s, want %s", got, want)
	}
}

func TestNewCancelled(t *testing.T) {
	got := string(NewCancelled())
	want := `{"event":"cancelled"}`
	if got != want {
		t.Errorf("NewCancelled() = %s, want %s", got, want)
	}
}

func TestNewUsageDelta(t *testing.T) {
	got := string(NewUsageDelta(10, 5, 15))
	want := `{"event":"usage_delta","input_tokens":10,"output_tokens":5,"total_tokens":15}`
	if got != want {
		t.Errorf("NewUsageDelta() = %s, want %s", got, want)
	}
}

func TestNewThinking(t *testing.T) {
	got := string(NewThinking("reasoning..."))
	want := `{"event":"thinking","text":"reasoning..."}`
	if got != want {
		t.Errorf("NewThinking() = %s, want %s", got, want)
	}
}

func TestNewFilesChanged(t *testing.T) {
	got := string(NewFilesChanged([]string{"a.txt", "b.go"}))
	want := `{"event":"files_changed","files":["a.txt","b.go"]}`
	if got != want {
		t.Errorf("NewFilesChanged() = %s, want %s", got, want)
	}
}

func TestNewFilesChangedEmpty(t *testing.T) {
	got := string(NewFilesChanged(nil))
	want := `{"event":"files_changed","files":null}`
	if got != want {
		t.Errorf("NewFilesChanged(nil) = %s, want %s", got, want)
	}
}
