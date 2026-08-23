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
	got := string(NewTurnEnd("END_TURN", &ctx, map[string]any{"credits": 0.08}, "model-x"))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":2.5,"metering":{"credits":0.08},"model":"model-x"}`
	if got != want {
		t.Errorf("NewTurnEnd() = %s, want %s", got, want)
	}
}

func TestNewTurnEndNilContext(t *testing.T) {
	got := string(NewTurnEnd("END_TURN", nil, nil, ""))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":null}`
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

func TestNewStatus(t *testing.T) {
	usage := Usage{Current: 1, Limit: 2, CurrentPrecise: 1.5}
	ctx := 3.5
	got := string(NewStatus(usage, &ctx))
	want := `{"event":"status","usage":{"current":1,"limit":2,"current_precise":1.5},"context_pct":3.5}`
	if got != want {
		t.Errorf("NewStatus() = %s, want %s", got, want)
	}
}

func TestNewStatusNilContext(t *testing.T) {
	usage := Usage{Current: 0, Limit: 0, CurrentPrecise: 0}
	got := string(NewStatus(usage, nil))
	want := `{"event":"status","usage":{"current":0,"limit":0,"current_precise":0},"context_pct":null}`
	if got != want {
		t.Errorf("NewStatus(nil) = %s, want %s", got, want)
	}
}
