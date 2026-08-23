package protocol

import "encoding/json"

type InboundCommand struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

func ParseInbound(data []byte) (*InboundCommand, error) {
	var cmd InboundCommand
	if err := json.Unmarshal(data, &cmd); err != nil {
		return nil, err
	}
	return &cmd, nil
}

type Ready struct {
	Event Event `json:"event"`
}

type Chunk struct {
	Event Event  `json:"event"`
	Text  string `json:"text"`
}

type ToolCallEvent struct {
	Event Event           `json:"event"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type TurnEnd struct {
	Event        Event    `json:"event"`
	StopReason   string   `json:"stop_reason"`
	ContextPct   *float64 `json:"context_pct"`
	Model        string   `json:"model,omitempty"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	TotalTokens  int      `json:"total_tokens"`
}

type ErrorEvent struct {
	Event   Event  `json:"event"`
	Message string `json:"message"`
}

type Cancelled struct {
	Event Event `json:"event"`
}


type UsageDelta struct {
	Event        Event `json:"event"`
	InputTokens  int   `json:"input_tokens"`
	OutputTokens int   `json:"output_tokens"`
	TotalTokens  int   `json:"total_tokens"`
}

func NewReady() []byte {
	evt := Ready{Event: EventReady}
	b, _ := json.Marshal(evt)
	return b
}

func NewChunk(text string) []byte {
	evt := Chunk{Event: EventChunk, Text: text}
	b, _ := json.Marshal(evt)
	return b
}

func NewToolCall(id, name string, input any) ([]byte, error) {
	rawInput, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	evt := ToolCallEvent{Event: EventToolCall, ID: id, Name: name, Input: rawInput}
	return json.Marshal(evt)
}

func NewTurnEnd(stopReason string, contextPct *float64, model string, inputTokens, outputTokens, totalTokens int) []byte {
	evt := TurnEnd{Event: EventTurnEnd, StopReason: stopReason,
		ContextPct: contextPct, Model: model,
		InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}
	b, _ := json.Marshal(evt)
	return b
}

func NewError(message string) []byte {
	evt := ErrorEvent{Event: EventError, Message: message}
	b, _ := json.Marshal(evt)
	return b
}

func NewCancelled() []byte {
	evt := Cancelled{Event: EventCancelled}
	b, _ := json.Marshal(evt)
	return b
}


func NewUsageDelta(inputTokens, outputTokens, totalTokens int) []byte {
	evt := UsageDelta{Event: EventUsageDelta, InputTokens: inputTokens, OutputTokens: outputTokens, TotalTokens: totalTokens}
	b, _ := json.Marshal(evt)
	return b
}
