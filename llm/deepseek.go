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

// DefaultReasoningEffort is the reasoning_effort value sent to the API when
// thinking mode is enabled (the user confirmed the API accepts "high").
const DefaultReasoningEffort = "high"

type DeepSeekProvider struct {
	apiKey   string
	endpoint string
	model    string
	thinking bool
	// explicitModel reports whether model was explicitly configured (via
	// --model or DEEPSEEK_MODEL). When false, the model is derived from the
	// thinking mode (deepseek-flash / deepseek-chat).
	explicitModel bool
	// reasoningEffort is sent as reasoning_effort in the request body when
	// thinking is enabled. Empty means the parameter is not sent.
	reasoningEffort string
	client          *http.Client
}

// NewDeepSeekProvider creates a provider with thinking mode enabled by default
// and an explicitly configured model.
func NewDeepSeekProvider(apiKey, endpoint, model string) *DeepSeekProvider {
	return NewDeepSeekProviderWithThinking(apiKey, endpoint, model, true)
}

// NewDeepSeekProviderWithThinking creates a provider with an explicit thinking
// setting and an explicitly configured model. The model is always respected:
// the thinking mode only controls whether reasoning_effort is sent and, for
// auto-model providers, which default model is used.
func NewDeepSeekProviderWithThinking(apiKey, endpoint, model string, thinking bool) *DeepSeekProvider {
	return &DeepSeekProvider{
		apiKey:          apiKey,
		endpoint:        endpoint,
		model:           model,
		thinking:        thinking,
		explicitModel:   true,
		reasoningEffort: DefaultReasoningEffort,
		client:          &http.Client{},
	}
}

// NewDeepSeekProviderAutoModel creates a provider without an explicit model;
// the model is derived from the thinking mode (deepseek-flash when thinking
// is on, deepseek-chat when off).
func NewDeepSeekProviderAutoModel(apiKey, endpoint string, thinking bool) *DeepSeekProvider {
	return &DeepSeekProvider{
		apiKey:          apiKey,
		endpoint:        endpoint,
		thinking:        thinking,
		explicitModel:   false,
		reasoningEffort: DefaultReasoningEffort,
		client:          &http.Client{},
	}
}

// DefaultModel returns the DeepSeek model that matches the thinking mode:
// deepseek-flash enables thinking, deepseek-chat disables it.
func DefaultModel(thinking bool) string {
	if thinking {
		return "deepseek-flash"
	}
	return "deepseek-chat"
}

// parseBoolEnv parses an environment variable as a boolean, falling back to
// def when the variable is empty or has an unrecognized value.
func parseBoolEnv(key string, def bool) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	}
	return def
}

// ThinkingFromEnv reads DEEPSEEK_THINKING (true/false/1/0/yes/no/on/off),
// returning def when unset or unrecognized. Default thinking mode is true.
func ThinkingFromEnv(def bool) bool {
	return parseBoolEnv("DEEPSEEK_THINKING", def)
}

// NewDeepSeekProviderFromEnv creates a provider reading from environment variables:
//
//	DEEPSEEK_API_KEY           (required)
//	DEEPSEEK_URL               (optional, default https://api.deepseek.com/chat/completions)
//	DEEPSEEK_MODEL             (optional, explicit model — always respected)
//	DEEPSEEK_THINKING          (optional, default true — thinking mode)
//	DEEPSEEK_REASONING_EFFORT  (optional, default "high" — sent when thinking is on)
func NewDeepSeekProviderFromEnv() *DeepSeekProvider {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	endpoint := os.Getenv("DEEPSEEK_URL")
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	thinking := ThinkingFromEnv(true)
	model := os.Getenv("DEEPSEEK_MODEL")
	if model != "" {
		return NewDeepSeekProviderWithThinking(apiKey, endpoint, model, thinking)
	}
	return NewDeepSeekProviderAutoModel(apiKey, endpoint, thinking)
}

func (p *DeepSeekProvider) Name() string { return "deepseek" }

// Model returns the model used for requests. An explicitly configured model is
// always respected; otherwise the model is derived from the thinking mode.
func (p *DeepSeekProvider) Model() string {
	if p.explicitModel {
		return p.model
	}
	return DefaultModel(p.thinking)
}

// Thinking reports whether thinking mode is enabled for this provider.
func (p *DeepSeekProvider) Thinking() bool { return p.thinking }

// SetThinking updates the thinking mode of the provider. For auto-model
// providers this also changes the effective model (deepseek-flash /
// deepseek-chat). An explicitly configured model is never touched.
func (p *DeepSeekProvider) SetThinking(thinking bool) { p.thinking = thinking }

// ReasoningEffort returns the reasoning_effort value sent when thinking is on.
func (p *DeepSeekProvider) ReasoningEffort() string { return p.reasoningEffort }

// SetReasoningEffort overrides the reasoning_effort value (e.g. "low",
// "medium", "high"). An empty value disables sending the parameter.
func (p *DeepSeekProvider) SetReasoningEffort(effort string) { p.reasoningEffort = effort }

// effectiveThinking returns the thinking mode to use for a request, honoring a
// per-request override over the provider's configured mode.
func (p *DeepSeekProvider) effectiveThinking(req ChatRequest) bool {
	if req.Thinking != nil {
		return *req.Thinking
	}
	return p.thinking
}

// resolveModel returns the model to use for a request, honoring, in order of
// priority: an explicit per-request model (from a prompt), then an explicitly
// configured provider model, then the default model derived from the effective
// thinking mode.
func (p *DeepSeekProvider) resolveModel(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	if p.explicitModel {
		return p.model
	}
	return DefaultModel(p.effectiveThinking(req))
}

// effectiveReasoningEffort returns the reasoning_effort value to use for a
// request and whether it should be sent, honoring a per-request override over
// the provider's configured value. An empty value disables sending the
// parameter (both for a per-request override and the provider default).
func (p *DeepSeekProvider) effectiveReasoningEffort(req ChatRequest) (string, bool) {
	if req.ReasoningEffort != nil {
		return *req.ReasoningEffort, *req.ReasoningEffort != ""
	}
	return p.reasoningEffort, p.reasoningEffort != ""
}

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIMessageToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

// openAIContentPart is one element of the OpenAI-compatible multimodal content
// array: a text block or an image_url block.
type openAIContentPart struct {
	Type     string          `json:"type"`
	Text     string          `json:"text,omitempty"`
	ImageURL *openAIImageURL `json:"image_url,omitempty"`
}

// openAIImageURL is the image_url payload: either an inline data: URL or an
// external http(s) URL, plus an optional detail hint.
type openAIImageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type openAIMessage struct {
	Role string `json:"role"`
	// Content is a plain JSON string for text-only messages (the previous wire
	// format, byte-identical) or an array of content parts when the message
	// carries images. Omitted entirely when empty.
	Content          json.RawMessage         `json:"content,omitempty"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIMessageToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string                  `json:"tool_call_id,omitempty"`
}

// openAIContent builds the wire value for a message's content: nil (omitted)
// for an empty text-only message, a JSON string for a text-only message, or the
// OpenAI multimodal array of text/image_url parts when the message carries
// images. Images are attached only to user messages (the only role the API
// accepts them on); any image on another role is ignored.
func openAIContent(role, content string, images []Image) json.RawMessage {
	if role != RoleUser {
		images = nil
	}
	if len(images) == 0 {
		if content == "" {
			return nil
		}
		b, err := json.Marshal(content)
		if err != nil {
			// notest
			return nil
		}
		return b
	}
	parts := make([]openAIContentPart, 0, len(images)+1)
	if content != "" {
		parts = append(parts, openAIContentPart{Type: "text", Text: content})
	}
	for _, im := range images {
		u := im.URL
		if u == "" {
			u = im.DataURL()
		}
		parts = append(parts, openAIContentPart{
			Type:     "image_url",
			ImageURL: &openAIImageURL{URL: u, Detail: im.Detail},
		})
	}
	b, err := json.Marshal(parts)
	if err != nil {
		// notest
		return nil
	}
	return b
}

type openAIFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

// ThinkingOptions is the structured `thinking` parameter accepted by the
// DeepSeek API (v3 and v4 models). `type` is required: "disabled" turns the
// model's built-in reasoning off even for models that default to it
// (deepseek-v4-flash); "enabled" is the explicit way to turn it on.
type ThinkingOptions struct {
	Type string `json:"type"`
}

// streamOptions controls the streaming extras. include_usage makes DeepSeek
// emit a final chunk carrying the `usage` object (prompt/completion tokens and
// the prompt_cache_hit_tokens/prompt_cache_miss_tokens accounting). Without it
// the streamed response has no usage at all.
type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIRequest struct {
	Model           string           `json:"model"`
	Messages        []openAIMessage  `json:"messages"`
	Stream          bool             `json:"stream"`
	StreamOptions   *streamOptions   `json:"stream_options,omitempty"`
	Tools           []openAITool     `json:"tools,omitempty"`
	Thinking        *ThinkingOptions `json:"thinking,omitempty"`
	ReasoningEffort *string          `json:"reasoning_effort,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content          string `json:"content"`
			ReasoningContent string `json:"reasoning_content"`
		} `json:"message"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

type openAIToolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIStreamChunk struct {
	Choices []struct {
		Delta struct {
			ReasoningContent string                `json:"reasoning_content"`
			Content          string                `json:"content"`
			ToolCalls        []openAIToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

type toolCallBuilder struct {
	id   string
	name string
	args strings.Builder
}

func (p *DeepSeekProvider) Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: RoleSystem, Content: openAIContent(RoleSystem, req.System, nil)})
	}
	for _, m := range req.Messages {
		msg := openAIMessage{
			Role:       m.Role,
			Content:    openAIContent(m.Role, m.Content, m.Images),
			ToolCallID: m.ToolCallID,
		}
		// DeepSeek's thinking mode requires the assistant's reasoning_content to
		// be passed back to the API whenever that message performed tool calls
		// (otherwise the API returns a 400 error). For final answers without
		// tool calls it is ignored by the API, so we omit it to save tokens.
		if m.Role == RoleAssistant && len(m.ToolCalls) > 0 {
			msg.ReasoningContent = m.Reasoning
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, openAIMessageToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: openAIFunctionCall{
					Name:      tc.Name,
					Arguments: tc.Arguments,
				},
			})
		}
		msgs = append(msgs, msg)
	}

	logger.Tracef("DeepSeek request messages (model=%s):", p.model)
	for _, msg := range msgs {
		logger.Tracef("  role=%s content=%s tool_calls=%d tool_call_id=%q", msg.Role, string(msg.Content), len(msg.ToolCalls), msg.ToolCallID)
		for _, tc := range msg.ToolCalls {
			logger.Tracef("    tool_call id=%s name=%s arguments=%s", tc.ID, tc.Function.Name, tc.Function.Arguments)
		}
	}

	toolsPayload := make([]openAITool, 0, len(req.Tools))
	for _, t := range req.Tools {
		toolsPayload = append(toolsPayload, openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	logger.Tracef("DeepSeek request tools: %d", len(toolsPayload))
	for _, t := range toolsPayload {
		logger.Tracef("  tool type=%s name=%s", t.Type, t.Function.Name)
	}

	model := p.resolveModel(req)

	payload := openAIRequest{
		Model:         model,
		Messages:      msgs,
		Stream:        true,
		StreamOptions: &streamOptions{IncludeUsage: true},
		Tools:         toolsPayload,
	}
	// Thinking mode is sent explicitly via the structured `thinking` parameter
	// (accepted by v3 and v4 models):
	//   - thinking off → `thinking: {"type":"disabled"}`. This is required for
	//     models like deepseek-v4-flash that reason BY DEFAULT even without any
	//     parameter (just omitting reasoning_effort is not enough to stop them).
	//   - thinking on  → `thinking: {"type":"enabled"}` plus the top-level
	//     reasoning_effort (e.g. "high") that controls the strength of the
	//     thinking/reasoning stage. A per-request override wins over the
	//     provider's configured value.
	thinking := p.effectiveThinking(req)
	effort, _ := p.effectiveReasoningEffort(req)
	logger.Tracef("DeepSeek request reasoning: thinking=%v reasoning_effort=%q", thinking, effort)
	if thinking {
		payload.Thinking = &ThinkingOptions{Type: "enabled"}
		if effort, ok := p.effectiveReasoningEffort(req); ok {
			payload.ReasoningEffort = &effort
		}
	} else {
		payload.Thinking = &ThinkingOptions{Type: "disabled"}
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
		logger.Errorf("deepseek request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Errorf("deepseek HTTP %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var usage *Usage
	stopReason := "END_TURN"
	toolBuilders := map[int]*toolCallBuilder{}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			break
		}

		var chunk openAIStreamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// ignore malformed line
			continue
		}

		if len(chunk.Choices) > 0 {
			choice := chunk.Choices[0]

			// The chain-of-thought (reasoning_content) streams in before the
			// final content; keep it separate from the visible answer.
			if choice.Delta.ReasoningContent != "" {
				fullReasoning.WriteString(choice.Delta.ReasoningContent)
				if req.OnReasoning != nil {
					req.OnReasoning(choice.Delta.ReasoningContent)
				}
			}

			if choice.Delta.Content != "" {
				fullContent.WriteString(choice.Delta.Content)
				if onChunk != nil {
					onChunk(choice.Delta.Content)
				}
			}

			for _, dtc := range choice.Delta.ToolCalls {
				b := toolBuilders[dtc.Index]
				if b == nil {
					b = &toolCallBuilder{}
					toolBuilders[dtc.Index] = b
				}
				if dtc.ID != "" {
					b.id = dtc.ID
				}
				if dtc.Function.Name != "" {
					b.name = dtc.Function.Name
				}
				b.args.WriteString(dtc.Function.Arguments)
			}

			if choice.FinishReason != "" {
				stopReason = choice.FinishReason
			}
		}

		if chunk.Usage != nil {
			usage = chunk.Usage
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	indices := make([]int, 0, len(toolBuilders))
	for idx := range toolBuilders {
		indices = append(indices, idx)
	}
	sort.Ints(indices)
	toolCalls := make([]ToolCall, 0, len(indices))
	for _, idx := range indices {
		b := toolBuilders[idx]
		toolCalls = append(toolCalls, ToolCall{
			ID:        b.id,
			Name:      b.name,
			Arguments: b.args.String(),
		})
	}

	if usage == nil {
		usage = &Usage{}
	}

	return &ChatResponse{
		Content:    fullContent.String(),
		StopReason: stopReason,
		Usage:      usage,
		ToolCalls:  toolCalls,
		Model:      model,
		Reasoning:  fullReasoning.String(),
	}, nil
}
