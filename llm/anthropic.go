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
	// DefaultAnthropicModel is used when no model is configured.
	DefaultAnthropicModel = "claude-sonnet-5-5"
	// DefaultAnthropicVersion is the anthropic-version header value.
	DefaultAnthropicVersion = "2026-10-21"
	// DefaultAnthropicMaxTokens is max_tokens, a required field of the API.
	DefaultAnthropicMaxTokens = 8192
	// anthropicMinThinkingBudget is the smallest budget_tokens the API accepts.
	anthropicMinThinkingBudget = 1024
	// anthropicBudgetHeadroom is added to max_tokens when the reasoning budget
	// does not fit under it: budget_tokens has to be smaller than max_tokens.
	anthropicBudgetHeadroom = 8192
)

type AnthropicProvider struct {
	apiKey   string
	endpoint string
	version  string
	model    string
	// maxTokens is the required max_tokens of the request, raised when a
	// reasoning budget needs room under it.
	maxTokens int
	thinking  bool
	// thinkingBudget is the budget_tokens of the manual extended thinking mode.
	// 0 means adaptive thinking, where the model decides how much to think.
	thinkingBudget int
	// effort is the output_config.effort sent when a request asks for no level.
	// Empty stops sending the parameter.
	effort string
	client *http.Client
}

// NewAnthropicProvider creates a provider with thinking mode enabled by default
// and an explicit model.
func NewAnthropicProvider(apiKey, endpoint, model string) *AnthropicProvider {
	return NewAnthropicProviderWithThinking(apiKey, endpoint, model, true)
}

// NewAnthropicProviderWithThinking creates a provider with an explicit thinking
// setting.
func NewAnthropicProviderWithThinking(apiKey, endpoint, model string, thinking bool) *AnthropicProvider {
	if endpoint == "" {
		endpoint = "https://api.anthropic.com/v1/messages"
	}
	if model == "" {
		model = DefaultAnthropicModel
	}
	return &AnthropicProvider{
		apiKey:    apiKey,
		endpoint:  endpoint,
		version:   DefaultAnthropicVersion,
		model:     model,
		maxTokens: DefaultAnthropicMaxTokens,
		thinking:  thinking,
		client:    &http.Client{},
	}
}

// AnthropicThinkingFromEnv reads ANTHROPIC_THINKING, returning def when unset.
func AnthropicThinkingFromEnv(def bool) bool {
	return parseBoolEnv("ANTHROPIC_THINKING", def)
}

// NewAnthropicProviderFromEnv creates a provider reading from environment
// variables:
//
//	ANTHROPIC_API_KEY         (required)
//	ANTHROPIC_URL             (optional, default https://api.anthropic.com/v1/messages)
//	ANTHROPIC_VERSION         (optional, default 2026-10-21)
//	ANTHROPIC_MODEL           (optional, default claude-sonnet-5-5)
//	ANTHROPIC_MAX_TOKENS      (optional, default 8192)
//	ANTHROPIC_THINKING        (optional, default true)
//	ANTHROPIC_THINKING_BUDGET (optional, a number selects manual extended
//	                           thinking; unset keeps adaptive thinking)
func NewAnthropicProviderFromEnv() *AnthropicProvider {
	p := NewAnthropicProviderWithThinking(
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("ANTHROPIC_URL"),
		os.Getenv("ANTHROPIC_MODEL"),
		AnthropicThinkingFromEnv(true),
	)
	if v := os.Getenv("ANTHROPIC_VERSION"); v != "" {
		p.version = v
	}
	if v := os.Getenv("ANTHROPIC_MAX_TOKENS"); v != "" {
		p.maxTokens = ParseBudget(v, p.maxTokens)
	}
	if v, ok := os.LookupEnv("ANTHROPIC_THINKING_BUDGET"); ok {
		p.thinkingBudget = ParseBudget(v, p.thinkingBudget)
	}
	return p
}

func (p *AnthropicProvider) Name() string { return "anthropic" }

// Model returns the model used for requests.
func (p *AnthropicProvider) Model() string { return p.model }

// Thinking reports whether thinking mode is enabled for this provider.
func (p *AnthropicProvider) Thinking() bool { return p.thinking }

// SetThinking updates the thinking mode of the provider.
func (p *AnthropicProvider) SetThinking(thinking bool) { p.thinking = thinking }

// ThinkingBudget returns the budget_tokens used by the manual thinking mode, or
// 0 for adaptive thinking.
func (p *AnthropicProvider) ThinkingBudget() int { return p.thinkingBudget }

// SetThinkingBudget overrides the budget_tokens. 0 selects adaptive thinking.
func (p *AnthropicProvider) SetThinkingBudget(budget int) { p.thinkingBudget = budget }

// Version returns the anthropic-version header value.
func (p *AnthropicProvider) Version() string { return p.version }

// SetVersion overrides the anthropic-version header value.
func (p *AnthropicProvider) SetVersion(version string) {
	if version != "" {
		p.version = version
	}
}

// MaxTokens returns the max_tokens sent in requests.
func (p *AnthropicProvider) MaxTokens() int { return p.maxTokens }

// SetMaxTokens overrides max_tokens, a required field of the API.
func (p *AnthropicProvider) SetMaxTokens(n int) {
	if n > 0 {
		p.maxTokens = n
	}
}

// effectiveThinking returns the thinking mode for a request: the per-request
// override, or the configured one.
func (p *AnthropicProvider) effectiveThinking(req ChatRequest) bool {
	if req.Thinking != nil {
		return *req.Thinking
	}
	return p.thinking
}

// resolveModel returns the model for a request: the per-request model or the
// configured one.
func (p *AnthropicProvider) resolveModel(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return p.model
}

// effectiveEffort returns the output_config.effort for a request, and whether
// to send it. Anthropic knows the whole common scale, so the level goes out
// as it came.
func (p *AnthropicProvider) effectiveEffort(req ChatRequest) (string, bool) {
	if req.ReasoningLevel != nil {
		effort := string(*req.ReasoningLevel)
		return effort, effort != ""
	}
	return p.effort, p.effort != ""
}

// Effort returns the effort sent when a request asks for no level.
func (p *AnthropicProvider) Effort() string { return p.effort }

// SetEffort overrides the effort sent when a request asks for no level. An
// empty value stops sending the parameter.
func (p *AnthropicProvider) SetEffort(effort string) { p.effort = effort }

// effectiveThinkingConfig builds the thinking parameter: disabled when thinking
// is off, manual extended thinking when a budget was asked for, and adaptive
// otherwise.
func (p *AnthropicProvider) effectiveThinkingConfig(req ChatRequest) *anthropicThinking {
	if !p.effectiveThinking(req) {
		return &anthropicThinking{Type: "disabled"}
	}
	budget := p.thinkingBudget
	if req.ThinkingBudget > 0 {
		budget = req.ThinkingBudget
	}
	if budget <= 0 {
		// Adaptive: the model decides when and how much to think. Without
		// display the thinking text may not come back at all.
		return &anthropicThinking{Type: "adaptive", Display: "summarized"}
	}
	if budget < anthropicMinThinkingBudget {
		budget = anthropicMinThinkingBudget
	}
	return &anthropicThinking{Type: "enabled", Budget: budget, Display: "summarized"}
}

// effectiveMaxTokens returns max_tokens for a request, raised above the
// reasoning budget when the budget would not fit under it.
func (p *AnthropicProvider) effectiveMaxTokens(req ChatRequest) int {
	maxTokens := p.maxTokens
	thinking := p.effectiveThinkingConfig(req)
	if thinking.Budget > 0 && thinking.Budget >= maxTokens {
		maxTokens = thinking.Budget + anthropicBudgetHeadroom
	}
	return maxTokens
}

// anthropicImageSource is the image payload: base64 bytes with their media
// type, or a URL the API fetches.
type anthropicImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

// anthropicCacheControl marks a prompt cache breakpoint. The API caches
// everything before the mark and reads it back on the next request.
type anthropicCacheControl struct {
	Type string `json:"type"`
}

// cacheBreakpoint returns an ephemeral breakpoint. The entry expires on its own
// after a few minutes of inactivity.
func cacheBreakpoint() *anthropicCacheControl {
	return &anthropicCacheControl{Type: "ephemeral"}
}

// anthropicBlock is one content block, in a request or in a response. The type
// decides which of the remaining fields mean anything.
type anthropicBlock struct {
	Type string `json:"type"`
	// CacheControl is only sent on a request block, at a breakpoint.
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
	// text
	Text string `json:"text,omitempty"`
	// thinking
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`
	// redacted_thinking
	Data string `json:"data,omitempty"`
	// image
	Source *anthropicImageSource `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type anthropicMessage struct {
	Role    string           `json:"role"`
	Content []anthropicBlock `json:"content"`
}

// anthropicThinking is the thinking parameter: adaptive, enabled with a budget,
// or disabled.
type anthropicThinking struct {
	Type    string `json:"type"`
	Budget  int    `json:"budget_tokens,omitempty"`
	Display string `json:"display,omitempty"`
}

// anthropicOutputConfig carries the effort on the common scale.
type anthropicOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
	// CacheControl is only sent on the last tool, where it caches the whole
	// tool list.
	CacheControl *anthropicCacheControl `json:"cache_control,omitempty"`
}

type anthropicRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	// System is a block list rather than the bare string the API also takes,
	// because a breakpoint has nowhere to go on a string.
	System       []anthropicBlock       `json:"system,omitempty"`
	Messages     []anthropicMessage     `json:"messages"`
	Tools        []anthropicTool        `json:"tools,omitempty"`
	Thinking     *anthropicThinking     `json:"thinking,omitempty"`
	OutputConfig *anthropicOutputConfig `json:"output_config,omitempty"`
	Stream       bool                   `json:"stream"`
}

// anthropicUsage is the usage object: input, output and the two cache counters.
type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// toUsage folds the cache counters into the neutral ones. The cache reads and
// the cache writes were both billed as prompt tokens, so they are part of
// PromptTokens.
func (u *anthropicUsage) toUsage() *Usage {
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	return &Usage{
		PromptTokens:     prompt,
		CompletionTokens: u.OutputTokens,
		TotalTokens:      prompt + u.OutputTokens,
		CachedTokens:     u.CacheReadInputTokens,
	}
}

// anthropicCost prices a call, or returns nil for a model without a price.
// Cache writes have their own rate, and the prompt length, cache included,
// picks the tier.
func anthropicCost(model string, u *anthropicUsage) *float64 {
	p, ok := LookupPrice(model)
	if !ok {
		return nil
	}
	prompt := u.InputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens
	r := p.RatesFor(prompt)
	write := r.CacheWrite
	if write == 0 {
		write = r.Input
	}
	cost := (float64(u.InputTokens)*r.Input +
		float64(u.CacheReadInputTokens)*r.Cached +
		float64(u.CacheCreationInputTokens)*write +
		float64(u.OutputTokens)*r.Output) / 1e6
	return &cost
}

// anthropicStreamEvent is one SSE event. The type decides which field is set.
type anthropicStreamEvent struct {
	Type    string `json:"type"`
	Index   int    `json:"index"`
	Message *struct {
		Model string          `json:"model"`
		Usage *anthropicUsage `json:"usage"`
	} `json:"message"`
	ContentBlock *anthropicBlock `json:"content_block"`
	Delta        *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		Signature   string `json:"signature"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`
	Usage *anthropicUsage `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicStopReason maps stop_reason onto the common vocabulary. The values
// outside it (stop_sequence, refusal, pause_turn) are passed through.
func anthropicStopReason(reason string) string {
	switch reason {
	case "end_turn":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	}
	return reason
}

// anthropicToolJSON returns the input of a tool_use block as raw JSON. The API
// sends an object, and an empty input has to reach it as {}.
func anthropicToolJSON(raw string) json.RawMessage {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return json.RawMessage("{}")
	}
	return json.RawMessage(trimmed)
}

// anthropicTextContent quotes a tool result as the string content the API
// takes: a tool_result accepts a plain string or an array of blocks.
func anthropicTextContent(content string) json.RawMessage {
	if content == "" {
		return json.RawMessage(`""`)
	}
	b, err := json.Marshal(content)
	if err != nil {
		// notest
		return json.RawMessage(`""`)
	}
	return b
}

// buildAnthropicMessages converts the history into the block form the API
// takes. Three shapes differ from the other providers: an assistant message is
// a list of blocks (thinking, text, tool_use), a tool result is a block inside
// a user message, and the consecutive tool results of one assistant message
// share that single user message.
func (p *AnthropicProvider) buildAnthropicMessages(messages []Message) []anthropicMessage {
	out := make([]anthropicMessage, 0, len(messages))
	for _, m := range messages {
		switch m.Role {
		case RoleSystem:
			// The system prompt is top level, so a system entry joins the
			// blocks of the user turn instead of becoming a message.
			if m.Content == "" {
				continue
			}
			out = append(out, anthropicMessage{Role: RoleUser, Content: []anthropicBlock{
				{Type: "text", Text: m.Content},
			}})

		case RoleAssistant:
			blocks := make([]anthropicBlock, 0, len(m.ToolCalls)+2)
			// The thinking block goes back unmodified and first, with the
			// signature the API emitted, or it answers 400.
			if m.Reasoning != "" {
				blocks = append(blocks, anthropicBlock{
					Type:      "thinking",
					Thinking:  m.Reasoning,
					Signature: m.Signature,
				})
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, anthropicBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Name,
					Input: anthropicToolJSON(tc.Arguments),
				})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicMessage{Role: RoleAssistant, Content: blocks})

		case "tool":
			result := anthropicBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   anthropicTextContent(m.Content),
			}
			// Answer the previous assistant message's calls in that same user
			// turn: consecutive tool results are blocks of one message.
			if n := len(out); n > 0 && out[n-1].Role == RoleUser && isToolResultMessage(out[n-1]) {
				out[n-1].Content = append(out[n-1].Content, result)
				continue
			}
			out = append(out, anthropicMessage{Role: RoleUser, Content: []anthropicBlock{result}})

		default: // RoleUser and any fallback
			blocks := make([]anthropicBlock, 0, len(m.Images)+1)
			for _, im := range m.Images {
				source := &anthropicImageSource{Type: "base64", MediaType: im.MIME, Data: im.Data}
				if im.URL != "" {
					source = &anthropicImageSource{Type: "url", URL: im.URL}
				}
				blocks = append(blocks, anthropicBlock{Type: "image", Source: source})
			}
			if m.Content != "" {
				blocks = append(blocks, anthropicBlock{Type: "text", Text: m.Content})
			}
			if len(blocks) == 0 {
				continue
			}
			out = append(out, anthropicMessage{Role: RoleUser, Content: blocks})
		}
	}
	return out
}

// markMessageBreakpoint puts a breakpoint on the last block of the last
// message. A thinking block ends an assistant message, and its signature is the
// fragile part, so the breakpoint walks back to the last block carrying content.
func markMessageBreakpoint(messages []anthropicMessage) {
	if len(messages) == 0 {
		return
	}
	content := messages[len(messages)-1].Content
	for i := len(content) - 1; i >= 0; i-- {
		switch content[i].Type {
		case "thinking", "redacted_thinking":
			continue
		}
		content[i].CacheControl = cacheBreakpoint()
		return
	}
}

// isToolResultMessage reports whether every block of msg is a tool_result.
func isToolResultMessage(msg anthropicMessage) bool {
	if len(msg.Content) == 0 {
		return false
	}
	for _, b := range msg.Content {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

// anthropicBlockBuilder accumulates the deltas of one content block.
type anthropicBlockBuilder struct {
	kind      string
	id        string
	name      string
	text      strings.Builder
	thinking  strings.Builder
	signature strings.Builder
	args      strings.Builder
}

func (p *AnthropicProvider) Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := p.resolveModel(req)
	messages := p.buildAnthropicMessages(req.Messages)

	payload := anthropicRequest{
		Model:     model,
		MaxTokens: p.effectiveMaxTokens(req),
		Messages:  messages,
		Stream:    true,
	}
	// Three breakpoints: the tool list, the system prompt and the last message.
	// The cache is a prefix, so the tool list one covers everything up to it and
	// the system prompt one covers both.
	if req.System != "" {
		payload.System = []anthropicBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: cacheBreakpoint(),
		}}
	}
	if len(req.Tools) > 0 {
		tools := make([]anthropicTool, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, anthropicTool{
				Name:        t.Name,
				Description: t.Description,
				InputSchema: t.Parameters,
			})
		}
		tools[len(tools)-1].CacheControl = cacheBreakpoint()
		payload.Tools = tools
	}
	markMessageBreakpoint(messages)
	payload.Thinking = p.effectiveThinkingConfig(req)
	if effort, ok := p.effectiveEffort(req); ok {
		payload.OutputConfig = &anthropicOutputConfig{Effort: effort}
	}

	logger.Tracef("Anthropic request model=%s max_tokens=%d messages=%d tools=%d thinking=%s",
		model, payload.MaxTokens, len(messages), len(payload.Tools), payload.Thinking.Type)
	if payload.Thinking.Budget > 0 {
		logger.Tracef("Anthropic thinking budget_tokens=%d", payload.Thinking.Budget)
	}
	if payload.OutputConfig != nil {
		logger.Tracef("Anthropic output_config effort=%s", payload.OutputConfig.Effort)
	}
	for _, m := range messages {
		logger.Tracef("  role=%s blocks=%d", m.Role, len(m.Content))
		for _, b := range m.Content {
			switch b.Type {
			case "tool_use":
				logger.Tracef("    tool_use id=%s name=%s input=%s", b.ID, b.Name, string(b.Input))
			case "tool_result":
				logger.Tracef("    tool_result tool_use_id=%s is_error=%v", b.ToolUseID, b.IsError)
			case "image":
				logger.Tracef("    image source=%s", b.Source.Type)
			case "thinking":
				logger.Tracef("    thinking signature_len=%d", len(b.Signature))
			default:
				logger.Tracef("    %s text_len=%d", b.Type, len(b.Text))
			}
		}
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
	httpReq.Header.Set("x-api-key", p.apiKey)
	httpReq.Header.Set("anthropic-version", p.version)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		logger.Errorf("anthropic request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Errorf("anthropic HTTP %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var fullSignature strings.Builder
	var usage *anthropicUsage
	stopReason := StopEndTurn
	builders := map[int]*anthropicBlockBuilder{}
	responseModel := model

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

		var ev anthropicStreamEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			logger.Warningf("anthropic: failed to unmarshal event: %v", err)
			continue
		}

		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				if ev.Message.Model != "" {
					responseModel = ev.Message.Model
				}
				if ev.Message.Usage != nil {
					usage = ev.Message.Usage
				}
			}

		case "content_block_start":
			if ev.ContentBlock != nil {
				builders[ev.Index] = &anthropicBlockBuilder{
					kind: ev.ContentBlock.Type,
					id:   ev.ContentBlock.ID,
					name: ev.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			b := builders[ev.Index]
			if b == nil {
				b = &anthropicBlockBuilder{kind: "text"}
				builders[ev.Index] = b
			}
			switch ev.Delta.Type {
			case "text_delta":
				b.text.WriteString(ev.Delta.Text)
				if onChunk != nil {
					onChunk(ev.Delta.Text)
				}
			case "thinking_delta":
				b.thinking.WriteString(ev.Delta.Thinking)
				if ev.Delta.Thinking != "" {
					fullReasoning.WriteString(ev.Delta.Thinking)
					if req.OnReasoning != nil {
						req.OnReasoning(ev.Delta.Thinking)
					}
				}
			case "signature_delta":
				b.signature.WriteString(ev.Delta.Signature)
			case "input_json_delta":
				b.args.WriteString(ev.Delta.PartialJSON)
			}

		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				stopReason = anthropicStopReason(ev.Delta.StopReason)
			}
			// The usage of this event is cumulative, so it replaces the one
			// from message_start instead of adding to it.
			if ev.Usage != nil {
				merged := anthropicUsage{}
				if usage != nil {
					merged = *usage
				}
				if ev.Usage.InputTokens > 0 {
					merged.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					merged.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					merged.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				merged.OutputTokens = ev.Usage.OutputTokens
				usage = &merged
			}

		case "error":
			if ev.Error != nil {
				return nil, fmt.Errorf("anthropic api error: %s: %s", ev.Error.Type, ev.Error.Message)
			}
			return nil, fmt.Errorf("anthropic api error")

		case "message_stop":
			// End of the stream.
		}

		if ev.Type == "message_stop" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	indices := make([]int, 0, len(builders))
	for idx := range builders {
		indices = append(indices, idx)
	}
	sort.Ints(indices)

	toolCalls := make([]ToolCall, 0, len(indices))
	for _, idx := range indices {
		b := builders[idx]
		switch b.kind {
		case "text":
			fullContent.WriteString(b.text.String())
		case "thinking":
			// Already streamed above; the signature is what has to be kept.
			fullSignature.WriteString(b.signature.String())
		case "tool_use":
			toolCalls = append(toolCalls, ToolCall{
				ID:        b.id,
				Name:      b.name,
				Arguments: b.args.String(),
			})
		}
	}

	if usage == nil {
		usage = &anthropicUsage{}
	}

	neutral := usage.toUsage()
	neutral.CostUSD = anthropicCost(responseModel, usage)

	return &ChatResponse{
		Content:    fullContent.String(),
		StopReason: stopReason,
		Usage:      neutral,
		ToolCalls:  toolCalls,
		Model:      responseModel,
		Reasoning:  fullReasoning.String(),
		Signature:  fullSignature.String(),
	}, nil
}
