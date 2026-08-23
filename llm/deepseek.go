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

type DeepSeekProvider struct {
	apiKey   string
	endpoint string
	model    string
	client   *http.Client
}

func NewDeepSeekProvider(apiKey, endpoint, model string) *DeepSeekProvider {
	return &DeepSeekProvider{
		apiKey:   apiKey,
		endpoint: endpoint,
		model:    model,
		client:   &http.Client{},
	}
}

// NewDeepSeekProviderFromEnv creates a provider reading from environment variables:
//
//	DEEPSEEK_API_KEY  (required)
//	DEEPSEEK_URL      (optional, default https://api.deepseek.com/chat/completions)
//	DEEPSEEK_MODEL    (optional, default deepseek-chat)
func NewDeepSeekProviderFromEnv() *DeepSeekProvider {
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	endpoint := os.Getenv("DEEPSEEK_URL")
	if endpoint == "" {
		endpoint = "https://api.deepseek.com/chat/completions"
	}
	model := os.Getenv("DEEPSEEK_MODEL")
	if model == "" {
		model = "deepseek-chat"
	}
	return NewDeepSeekProvider(apiKey, endpoint, model)
}

func (p *DeepSeekProvider) Name() string { return "deepseek" }

func (p *DeepSeekProvider) Model() string { return p.model }

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
	Role       string                  `json:"role"`
	Content    string                  `json:"content,omitempty"`
	ToolCalls  []openAIMessageToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
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
	Model    string          `json:"model"`
	Messages []openAIMessage `json:"messages"`
	Stream   bool            `json:"stream"`
	Tools    []openAITool    `json:"tools,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
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
			Content   string                `json:"content"`
			ToolCalls []openAIToolCallDelta `json:"tool_calls"`
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

	payload := openAIRequest{
		Model:    p.model,
		Messages: msgs,
		Stream:   true,
		Tools:    toolsPayload,
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
	}, nil
}
