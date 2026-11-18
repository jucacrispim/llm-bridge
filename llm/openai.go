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

package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"llm-bridge/logger"
)

const (
	// DefaultOpenAIModel is used when no model is configured.
	DefaultOpenAIModel = "gpt-6.2-sol"
	// DefaultOpenAIEffort is the reasoning effort sent when thinking is on and
	// no level was configured.
	DefaultOpenAIEffort = "medium"
	// openaiReasoningInclude asks for the encrypted reasoning item. The bridge
	// sets store: false, so the item is only replayable with this include.
	openaiReasoningInclude = "reasoning.encrypted_content"
)

type OpenAIProvider struct {
	apiKey   string
	endpoint string
	model    string
	thinking bool
	// reasoningEffort goes in reasoning.effort when thinking is on. Empty
	// means the field is not sent and the API picks its own default.
	reasoningEffort string
	// reasoningSummary turns reasoning.summary on ("auto"). Empty leaves it
	// out, and the reasoning text does not come back at all.
	reasoningSummary string
	// maxOutputTokens is max_output_tokens. 0 means the field is not sent.
	maxOutputTokens int
	client          *http.Client
}

// NewOpenAIProvider creates a provider with thinking mode enabled by default
// and an explicit model.
func NewOpenAIProvider(apiKey, endpoint, model string) *OpenAIProvider {
	return NewOpenAIProviderWithThinking(apiKey, endpoint, model, true)
}

// NewOpenAIProviderWithThinking creates a provider with an explicit thinking
// setting.
func NewOpenAIProviderWithThinking(apiKey, endpoint, model string, thinking bool) *OpenAIProvider {
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/responses"
	}
	if model == "" {
		model = DefaultOpenAIModel
	}
	return &OpenAIProvider{
		apiKey:          apiKey,
		endpoint:        endpoint,
		model:           model,
		thinking:        thinking,
		reasoningEffort: DefaultOpenAIEffort,
		client:          &http.Client{},
	}
}

// OpenAIThinkingFromEnv reads OPENAI_THINKING, returning def when unset.
func OpenAIThinkingFromEnv(def bool) bool {
	return parseBoolEnv("OPENAI_THINKING", def)
}

// NewOpenAIProviderFromEnv creates a provider reading from environment
// variables:
//
//	OPENAI_API_KEY           (required)
//	OPENAI_URL               (optional, default https://api.openai.com/v1/responses)
//	OPENAI_MODEL             (optional, default gpt-6.2-sol)
//	OPENAI_THINKING          (optional, default true)
//	OPENAI_REASONING_EFFORT  (optional, default medium)
//	OPENAI_REASONING_SUMMARY (optional, "auto" asks for the summary of the
//	                          reasoning; unset keeps it off)
//	OPENAI_MAX_OUTPUT_TOKENS (optional, unset lets the API decide)
func NewOpenAIProviderFromEnv() *OpenAIProvider {
	p := NewOpenAIProviderWithThinking(
		os.Getenv("OPENAI_API_KEY"),
		os.Getenv("OPENAI_URL"),
		os.Getenv("OPENAI_MODEL"),
		OpenAIThinkingFromEnv(true),
	)
	if v := os.Getenv("OPENAI_REASONING_EFFORT"); v != "" {
		p.reasoningEffort = v
	}
	p.reasoningSummary = os.Getenv("OPENAI_REASONING_SUMMARY")
	if v := os.Getenv("OPENAI_MAX_OUTPUT_TOKENS"); v != "" {
		p.maxOutputTokens = ParseBudget(v, 0)
	}
	return p
}

func (p *OpenAIProvider) Name() string { return "openai" }

// Model returns the model used for requests.
func (p *OpenAIProvider) Model() string { return p.model }

// Thinking reports whether thinking mode is enabled for this provider.
func (p *OpenAIProvider) Thinking() bool { return p.thinking }

// SetThinking updates the thinking mode of the provider.
func (p *OpenAIProvider) SetThinking(thinking bool) { p.thinking = thinking }

// ReasoningEffort returns the effort sent when a request asks for no level.
func (p *OpenAIProvider) ReasoningEffort() string { return p.reasoningEffort }

// SetReasoningEffort overrides the effort sent when a request asks for no
// level. An empty value stops sending it.
func (p *OpenAIProvider) SetReasoningEffort(effort string) { p.reasoningEffort = effort }

// ReasoningSummary returns the value sent in reasoning.summary, or "" when the
// field is not sent.
func (p *OpenAIProvider) ReasoningSummary() string { return p.reasoningSummary }

// SetReasoningSummary turns reasoning.summary on ("auto") or off ("").
func (p *OpenAIProvider) SetReasoningSummary(summary string) {
	p.reasoningSummary = summary
}

// MaxOutputTokens returns max_output_tokens, 0 when the field is not sent.
func (p *OpenAIProvider) MaxOutputTokens() int { return p.maxOutputTokens }

// SetMaxOutputTokens overrides max_output_tokens. 0 stops sending the field.
func (p *OpenAIProvider) SetMaxOutputTokens(n int) { p.maxOutputTokens = n }

// effectiveThinking returns the thinking mode for a request: the per-request
// override, or the configured one.
func (p *OpenAIProvider) effectiveThinking(req ChatRequest) bool {
	if req.Thinking != nil {
		return *req.Thinking
	}
	return p.thinking
}

// resolveModel returns the model for a request: the per-request model or the
// configured one.
func (p *OpenAIProvider) resolveModel(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return p.model
}

// effectiveEffort returns the reasoning.effort for a request, and whether to
// send it. OpenAI takes the whole common scale, so the level goes out as it
// came.
func (p *OpenAIProvider) effectiveEffort(req ChatRequest) (string, bool) {
	if req.ReasoningLevel != nil {
		effort := string(*req.ReasoningLevel)
		return effort, effort != ""
	}
	return p.reasoningEffort, p.reasoningEffort != ""
}

// openaiContentPart is one element of a message content array. User input is
// input_text and input_image, assistant output is output_text.
type openaiContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// ImageURL is a data: URL or an http(s) URL the API fetches.
	ImageURL string `json:"image_url,omitempty"`
}

// openaiAnnotatedText is a text part of an assistant message, which the API
// names output_text.
type openaiAnnotatedText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openaiInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// openaiMessageItem builds a message item. Text-only content goes as a plain
// string, which the API takes, and images force the part array.
func openaiMessageItem(role, content string, images []Image) json.RawMessage {
	if len(images) == 0 {
		b, err := json.Marshal(openaiInputMessage{
			Role:    role,
			Content: openaiQuote(content),
		})
		if err != nil {
			// notest
			return nil
		}
		return b
	}
	parts := make([]openaiContentPart, 0, len(images)+1)
	if content != "" {
		parts = append(parts, openaiContentPart{Type: "input_text", Text: content})
	}
	for _, im := range images {
		u := im.URL
		if u == "" {
			u = im.DataURL()
		}
		parts = append(parts, openaiContentPart{Type: "input_image", ImageURL: u})
	}
	raw, err := json.Marshal(parts)
	if err != nil {
		// notest
		return nil
	}
	b, err := json.Marshal(openaiInputMessage{Role: role, Content: raw})
	if err != nil {
		// notest
		return nil
	}
	return b
}

// openaiQuote quotes a string as a JSON value, so it can sit in a
// json.RawMessage field.
func openaiQuote(s string) json.RawMessage {
	b, err := json.Marshal(s)
	if err != nil {
		// notest
		return json.RawMessage(`""`)
	}
	return b
}

// openaiArguments returns the arguments of a call as the JSON string the API
// takes. An empty argument list has to reach it as {}.
func openaiArguments(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "{}"
	}
	return raw
}

// openaiFunctionCallItem is a tool call from a previous assistant turn.
func openaiFunctionCallItem(tc ToolCall) json.RawMessage {
	b, err := json.Marshal(struct {
		Type      string `json:"type"`
		CallID    string `json:"call_id"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	}{"function_call", tc.ID, tc.Name, openaiArguments(tc.Arguments)})
	if err != nil {
		// notest
		return nil
	}
	return b
}

// openaiFunctionCallOutputItem is the result of a tool call.
func openaiFunctionCallOutputItem(m Message) json.RawMessage {
	b, err := json.Marshal(struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Output string `json:"output"`
	}{"function_call_output", m.ToolCallID, m.Content})
	if err != nil {
		// notest
		return nil
	}
	return b
}

// openaiInput builds the flat item list of a request. Three shapes differ from
// the other providers: a tool call is an item of its own rather than a field of
// the assistant message, a tool result is a function_call_output item, and the
// reasoning item goes back first, before the calls it led to.
func openaiInput(messages []Message) []json.RawMessage {
	items := make([]json.RawMessage, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case RoleAssistant:
			// The reasoning item is replayed exactly as the API emitted it,
			// with the encrypted content, or the calls lose their context.
			if m.Signature != "" && json.Valid([]byte(m.Signature)) {
				items = append(items, json.RawMessage(m.Signature))
			}
			if m.Content != "" {
				raw, err := json.Marshal(openaiAnnotatedText{Type: "output_text", Text: m.Content})
				if err == nil {
					msg, err := json.Marshal(openaiInputMessage{
						Role:    RoleAssistant,
						Content: json.RawMessage("[" + string(raw) + "]"),
					})
					if err == nil {
						items = append(items, msg)
					}
				}
			}
			for _, tc := range m.ToolCalls {
				items = append(items, openaiFunctionCallItem(tc))
			}

		case "tool":
			items = append(items, openaiFunctionCallOutputItem(m))

		default: // RoleUser, RoleSystem and any fallback
			if m.Content == "" && len(m.Images) == 0 {
				continue
			}
			items = append(items, openaiMessageItem(m.Role, m.Content, m.Images))
		}
	}
	return items
}

type openaiRespTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// openaiRespReasoning is the reasoning parameter: effort plus the summary. A
// reasoning model rejects effort "none", so turning thinking off means leaving
// the whole parameter out.
type openaiRespReasoning struct {
	Effort  string `json:"effort,omitempty"`
	Summary string `json:"summary,omitempty"`
}

type openaiRespRequest struct {
	Model string `json:"model"`
	// Input is the flat item list: messages, function_call and
	// function_call_output items, one after the other.
	Input        []json.RawMessage    `json:"input"`
	Instructions string               `json:"instructions,omitempty"`
	Tools        []openaiRespTool     `json:"tools,omitempty"`
	Reasoning    *openaiRespReasoning `json:"reasoning,omitempty"`
	// MaxOutputTokens caps the output, the reasoning tokens included.
	MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
	Store           bool     `json:"store"`
	Stream          bool     `json:"stream"`
	Include         []string `json:"include,omitempty"`
}

// openaiRespUsage is the usage object of the Responses API.
type openaiRespUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
	OutputTokensDetails struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"output_tokens_details"`
}

func (u *openaiRespUsage) toUsage() *Usage {
	return &Usage{
		PromptTokens:     u.InputTokens,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.InputTokensDetails.CachedTokens,
	}
}

// openaiResponsesCost prices a call, or returns nil for a model without a
// price. The cache read has its own rate, and the prompt length picks the tier.
func openaiResponsesCost(model string, u *openaiRespUsage) *float64 {
	p, ok := LookupPrice(model)
	if !ok {
		return nil
	}
	r := p.RatesFor(u.InputTokens)
	uncached := u.InputTokens - u.InputTokensDetails.CachedTokens
	if uncached < 0 {
		uncached = 0
	}
	cost := (float64(uncached)*r.Input +
		float64(u.InputTokensDetails.CachedTokens)*r.Cached +
		float64(u.OutputTokens)*r.Output) / 1e6
	return &cost
}

// openaiError is the error object the API sends. The error event carries it at
// the top level, response.failed inside the response.
type openaiError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Param   string `json:"param"`
}

// openaiErrorMessage formats an error for the caller. The code goes first
// because it tells insufficient_quota from invalid_api_key without relying on
// the prose. A shape we cannot read is returned raw, so the caller sees
// something instead of an empty message.
func openaiErrorMessage(e *openaiError, raw string) string {
	if e != nil {
		switch {
		case e.Code != "" && e.Message != "":
			return e.Code + ": " + e.Message
		case e.Message != "":
			return e.Message
		case e.Code != "":
			return e.Code
		}
	}
	if raw != "" {
		return raw
	}
	return "no error detail in the event"
}

// openaiRespResponse is the response object of the stream: the one carried by
// response.created, response.completed and response.failed.
type openaiRespResponse struct {
	ID                string            `json:"id"`
	Model             string            `json:"model"`
	Status            string            `json:"status"`
	Output            []json.RawMessage `json:"output"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	Usage *openaiRespUsage `json:"usage"`
	Error *openaiError     `json:"error"`
}

// openaiRespItem is the item of an output_item event. The type decides which of
// the remaining fields mean anything.
type openaiRespItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// openaiStreamEvent is one SSE event of the stream.
type openaiStreamEvent struct {
	Type        string `json:"type"`
	Delta       string `json:"delta"`
	OutputIndex int    `json:"output_index"`
	// Item is kept raw: the reasoning item is replayed as it came, encrypted
	// content included, and a typed struct drops what it does not know.
	Item     json.RawMessage     `json:"item"`
	Response *openaiRespResponse `json:"response"`
	// error event: the object sits at the top level, not in a response.
	Error *openaiError `json:"error"`
}

// openaiStopReason maps the status of a finished response onto the common
// vocabulary. A completed response is end_turn until the calls show otherwise.
func openaiStopReason(status, incompleteReason string) string {
	if status == "incomplete" {
		if incompleteReason == "max_output_tokens" {
			return StopMaxTokens
		}
		return incompleteReason
	}
	return StopEndTurn
}

type openaiCallBuilder struct {
	callID string
	name   string
	args   strings.Builder
}

func (p *OpenAIProvider) Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := p.resolveModel(req)
	input := openaiInput(req.Messages)

	payload := openaiRespRequest{
		Model:        model,
		Input:        input,
		Instructions: req.System,
		Store:        false,
		Stream:       true,
	}
	if len(req.Tools) > 0 {
		tools := make([]openaiRespTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, openaiRespTool{
				Type:        "function",
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		payload.Tools = tools
	}
	thinking := p.effectiveThinking(req)
	if thinking {
		reasoning := &openaiRespReasoning{Summary: p.reasoningSummary}
		if effort, ok := p.effectiveEffort(req); ok {
			reasoning.Effort = effort
		}
		payload.Reasoning = reasoning
		// store: false means the reasoning item only comes back encrypted, and
		// it has to come back for the next request to replay it.
		payload.Include = []string{openaiReasoningInclude}
	}
	if p.maxOutputTokens > 0 {
		payload.MaxOutputTokens = p.maxOutputTokens
	}

	logger.Tracef("OpenAI request model=%s items=%d tools=%d thinking=%v store=false",
		model, len(input), len(payload.Tools), thinking)
	if payload.Reasoning != nil {
		logger.Tracef("OpenAI reasoning effort=%q summary=%q",
			payload.Reasoning.Effort, payload.Reasoning.Summary)
	}
	for _, raw := range input {
		logger.Tracef("  item=%s", string(raw))
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		logger.Errorf("openai request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Errorf("openai HTTP %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	// signature is the reasoning item as it came, replayed on the next request.
	var signature string
	var usage *openaiRespUsage
	stopReason := ""
	responseModel := model
	builders := map[int]*openaiCallBuilder{}

	scanner := bufio.NewScanner(resp.Body)
	// Raised above the default so a large tool-call payload fits.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}

		var ev openaiStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			logger.Warningf("openai: failed to unmarshal event: %v", err)
			continue
		}

		switch ev.Type {
		case "response.output_text.delta":
			fullContent.WriteString(ev.Delta)
			if onChunk != nil {
				onChunk(ev.Delta)
			}

		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			fullReasoning.WriteString(ev.Delta)
			if req.OnReasoning != nil {
				req.OnReasoning(ev.Delta)
			}

		case "response.function_call_arguments.delta":
			b := builders[ev.OutputIndex]
			if b == nil {
				b = &openaiCallBuilder{}
				builders[ev.OutputIndex] = b
			}
			b.args.WriteString(ev.Delta)

		case "response.output_item.done":
			if len(ev.Item) == 0 {
				continue
			}
			var item openaiRespItem
			if err := json.Unmarshal(ev.Item, &item); err != nil {
				logger.Warningf("openai: failed to unmarshal item: %v", err)
				continue
			}
			switch item.Type {
			case "function_call":
				// The finished item is the authority: it carries the call_id and
				// the whole argument string, the deltas only an index.
				builders[ev.OutputIndex] = &openaiCallBuilder{
					callID: item.CallID,
					name:   item.Name,
				}
				builders[ev.OutputIndex].args.WriteString(item.Arguments)
			case "reasoning":
				signature = string(ev.Item)
			}

		case "response.completed", "response.incomplete":
			if ev.Response == nil {
				continue
			}
			if ev.Response.Model != "" {
				responseModel = ev.Response.Model
			}
			if ev.Response.Usage != nil {
				usage = ev.Response.Usage
			}
			reason := ""
			if ev.Response.IncompleteDetails != nil {
				reason = ev.Response.IncompleteDetails.Reason
			}
			stopReason = openaiStopReason(ev.Response.Status, reason)

		case "response.failed":
			if ev.Response != nil && ev.Response.Error != nil {
				logger.Errorf("openai response.failed: %s", data)
				return nil, fmt.Errorf("openai api error: %s",
					openaiErrorMessage(ev.Response.Error, data))
			}
			logger.Errorf("openai response.failed without an error object: %s", data)
			return nil, fmt.Errorf("openai api error: %s", openaiErrorMessage(nil, data))

		case "error":
			logger.Errorf("openai error event: %s", data)
			return nil, fmt.Errorf("openai api error: %s", openaiErrorMessage(ev.Error, data))

		default:
			// A name we do not know is either a new event or a typo in the
			// ones we listen to. Both show up here instead of in silence.
			logger.Tracef("openai: ignored event type %q: %s", ev.Type, data)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	if stopReason == "" && fullContent.Len() == 0 && len(builders) == 0 {
		// The stream closed before response.completed, response.incomplete or
		// response.failed. Returning an empty turn here would hide the cut.
		return nil, fmt.Errorf("openai stream ended without a completion event")
	}

	indices := make([]int, 0, len(builders))
	for idx := range builders {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	toolCalls := make([]ToolCall, 0, len(indices))
	for _, idx := range indices {
		b := builders[idx]
		toolCalls = append(toolCalls, ToolCall{
			ID:        b.callID,
			Name:      b.name,
			Arguments: openaiArguments(b.args.String()),
		})
	}

	if stopReason == "" {
		stopReason = StopEndTurn
	}
	// A response that stopped on its own and carries calls ended for the tools,
	// unless it ran out of tokens first.
	if stopReason == StopEndTurn && len(toolCalls) > 0 {
		stopReason = StopToolUse
	}

	if usage == nil {
		usage = &openaiRespUsage{}
	}

	neutral := usage.toUsage()
	neutral.CostUSD = openaiResponsesCost(responseModel, usage)

	return &ChatResponse{
		Content:    fullContent.String(),
		StopReason: stopReason,
		Usage:      neutral,
		ToolCalls:  toolCalls,
		Model:      responseModel,
		Reasoning:  fullReasoning.String(),
		Signature:  signature,
	}, nil
}
