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
	Event      Event    `json:"event"`
	StopReason string   `json:"stop_reason"`
	ContextPct *float64 `json:"context_pct"`
	Metering   any      `json:"metering,omitempty"`
	Model      string   `json:"model,omitempty"`
}

type ErrorEvent struct {
	Event   Event  `json:"event"`
	Message string `json:"message"`
}

type Cancelled struct {
	Event Event `json:"event"`
}

type Usage struct {
	Current        int     `json:"current"`
	Limit          int     `json:"limit"`
	CurrentPrecise float64 `json:"current_precise"`
}

type Status struct {
	Event      Event    `json:"event"`
	Usage      Usage    `json:"usage"`
	ContextPct *float64 `json:"context_pct"`
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

func NewTurnEnd(stopReason string, contextPct *float64, metering any, model string) []byte {
	evt := TurnEnd{Event: EventTurnEnd, StopReason: stopReason,
		ContextPct: contextPct, Metering: metering, Model: model}
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

func NewStatus(usage Usage, contextPct *float64) []byte {
	evt := Status{Event: EventStatus, Usage: usage, ContextPct: contextPct}
	b, _ := json.Marshal(evt)
	return b
}
