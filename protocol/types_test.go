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
	got := string(NewTurnEnd("END_TURN", &ctx, "model-x", 10, 5, 15))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":2.5,"model":"model-x","input_tokens":10,"output_tokens":5,"total_tokens":15}`
	if got != want {
		t.Errorf("NewTurnEnd() = %s, want %s", got, want)
	}
}

func TestNewTurnEndNilContext(t *testing.T) {
	got := string(NewTurnEnd("END_TURN", nil, "", 0, 0, 0))
	want := `{"event":"turn_end","stop_reason":"END_TURN","context_pct":null,"input_tokens":0,"output_tokens":0,"total_tokens":0}`
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
