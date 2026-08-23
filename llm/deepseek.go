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
	// thinking mode (deepseek-reasoner / deepseek-chat).
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
// the model is derived from the thinking mode (deepseek-reasoner when thinking
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
// deepseek-reasoner enables thinking, deepseek-chat disables it.
func DefaultModel(thinking bool) string {
	if thinking {
		return "deepseek-reasoner"
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
// providers this also changes the effective model (deepseek-reasoner /
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

type openAIFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type openAIMessageToolCall struct {
	ID       string             `json:"id"`
	Type     string             `json:"type"`
	Function openAIFunctionCall `json:"function"`
}

type openAIMessage struct {
	Role             string                  `json:"role"`
	Content          string                  `json:"content,omitempty"`
	ReasoningContent string                  `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIMessageToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string                  `json:"tool_call_id,omitempty"`
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

type openAIRequest struct {
	Model           string          `json:"model"`
	Messages        []openAIMessage `json:"messages"`
	Stream          bool            `json:"stream"`
	Tools           []openAITool    `json:"tools,omitempty"`
	ReasoningEffort *string         `json:"reasoning_effort,omitempty"`
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
	msgs := make([]openAIMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := openAIMessage{
			Role:       m.Role,
			Content:    m.Content,
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
		logger.Tracef("  role=%s content=%q tool_calls=%d tool_call_id=%q", msg.Role, msg.Content, len(msg.ToolCalls), msg.ToolCallID)
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
		Model:    model,
		Messages: msgs,
		Stream:   true,
		Tools:    toolsPayload,
	}
	// The API accepts reasoning_effort (e.g. "high") to control the strength of
	// the thinking/reasoning stage. Send it whenever thinking is enabled.
	if p.effectiveThinking(req) && p.reasoningEffort != "" {
		effort := p.reasoningEffort
		payload.ReasoningEffort = &effort
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
