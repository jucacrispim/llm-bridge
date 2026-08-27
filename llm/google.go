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
	"strconv"
	"strings"

	"llm-bridge/logger"
)

// DefaultGoogleThinkingBudget is the thinkingConfig.thinkingBudget sent to the
// Gemini API when thinking mode is enabled. 0 disables thinking; a positive
// value allocates that many tokens to the model's reasoning stage.
const DefaultGoogleThinkingBudget = 1024

// DefaultGoogleModel is used when no explicit model is configured.
const DefaultGoogleModel = "gemini-2.5-flash"

type GoogleProvider struct {
	apiKey   string
	endpoint string
	model    string
	thinking bool
	// thinkingBudget is the thinkingConfig.thinkingBudget sent when thinking is
	// enabled. 0 disables thinking entirely (only used here when thinking is
	// off, since on forces a positive budget).
	thinkingBudget int
	client         *http.Client
}

// NewGoogleProvider creates a Gemini provider with thinking mode enabled by
// default and an explicitly configured model.
func NewGoogleProvider(apiKey, endpoint, model string) *GoogleProvider {
	return NewGoogleProviderWithThinking(apiKey, endpoint, model, true)
}

// NewGoogleProviderWithThinking creates a Gemini provider with an explicit
// thinking setting and an explicitly configured model.
func NewGoogleProviderWithThinking(apiKey, endpoint, model string, thinking bool) *GoogleProvider {
	if endpoint == "" {
		endpoint = "https://generativelanguage.googleapis.com/v1beta"
	}
	if model == "" {
		model = DefaultGoogleModel
	}
	return &GoogleProvider{
		apiKey:         apiKey,
		endpoint:       strings.TrimSuffix(endpoint, "/"),
		model:          model,
		thinking:       thinking,
		thinkingBudget: DefaultGoogleThinkingBudget,
		client:         &http.Client{},
	}
}

// GoogleThinkingFromEnv reads GOOGLE_THINKING (true/false/1/0/yes/no/on/off),
// returning def when unset or unrecognized.
func GoogleThinkingFromEnv(def bool) bool {
	return parseBoolEnv("GOOGLE_THINKING", def)
}

// NewGoogleProviderFromEnv creates a provider reading from environment variables:
//
//	GOOGLE_API_KEY         (required; GEMINI_API_KEY also honored)
//	GOOGLE_URL             (optional, default https://generativelanguage.googleapis.com/v1beta)
//	GOOGLE_MODEL           (optional, default gemini-2.5-flash; GEMINI_MODEL also honored)
//	GOOGLE_THINKING        (optional, default true — thinking mode)
//	GOOGLE_THINKING_BUDGET (optional, default 1024 — thinkingConfig.thinkingBudget)
func NewGoogleProviderFromEnv() *GoogleProvider {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("GEMINI_API_KEY")
	}
	endpoint := os.Getenv("GOOGLE_URL")
	model := os.Getenv("GOOGLE_MODEL")
	if model == "" {
		model = os.Getenv("GEMINI_MODEL")
	}
	p := NewGoogleProviderWithThinking(apiKey, endpoint, model, GoogleThinkingFromEnv(true))
	if budget := os.Getenv("GOOGLE_THINKING_BUDGET"); budget != "" {
		if n, err := strconv.Atoi(budget); err == nil {
			p.SetThinkingBudget(n)
		}
	}
	return p
}

func (p *GoogleProvider) Name() string { return "google" }

// Model returns the model used for requests.
func (p *GoogleProvider) Model() string { return p.model }

// Thinking reports whether thinking mode is enabled for this provider.
func (p *GoogleProvider) Thinking() bool { return p.thinking }

// SetThinking updates the thinking mode of the provider.
func (p *GoogleProvider) SetThinking(thinking bool) { p.thinking = thinking }

// ThinkingBudget returns the thinkingConfig.thinkingBudget used when thinking
// is enabled.
func (p *GoogleProvider) ThinkingBudget() int { return p.thinkingBudget }

// SetThinkingBudget overrides the thinkingConfig.thinkingBudget value. 0 means
// thinking is effectively disabled.
func (p *GoogleProvider) SetThinkingBudget(budget int) { p.thinkingBudget = budget }

// effectiveThinking returns the thinking mode to use for a request, honoring a
// per-request override over the provider's configured mode.
func (p *GoogleProvider) effectiveThinking(req ChatRequest) bool {
	if req.Thinking != nil {
		return *req.Thinking
	}
	return p.thinking
}

// resolveModel returns the model to use for a request, honoring, in order of
// priority: an explicit per-request model (from a prompt), then the provider's
// configured model.
func (p *GoogleProvider) resolveModel(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return p.model
}

// ParseBudget parses a string as an integer, returning def when it is not a
// valid non-negative number. Used to read the thinkingConfig.thinkingBudget
// from a flag or environment variable.
func ParseBudget(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
		return n
	}
	return def
}

// effectiveThinkingBudget returns the thinkingConfig.thinkingBudget to send for
// a request, honoring a per-request reasoning effort override that parses as a
// numeric budget over the provider's configured value. When thinking is off the
// budget is always 0 (thinking disabled).
func (p *GoogleProvider) effectiveThinkingBudget(req ChatRequest) int {
	if !p.effectiveThinking(req) {
		return 0
	}
	if req.ReasoningEffort != nil {
		if n, err := strconv.Atoi(*req.ReasoningEffort); err == nil {
			return n
		}
	}
	return p.thinkingBudget
}

// googleFunctionCall is Gemini's function-call part payload. The model uses
// `args` (an object), unlike OpenAI's JSON-string `arguments`. The
// thought_signature is NOT a field of this object: when thinking mode is on,
// Gemini emits it as a SIBLING field of the part (googlePart.ThoughtSignature)
// and requires it to be echoed back the same way.
type googleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// googleFunctionResponse is the payload of a tool result sent back to Gemini as
// a user-role part. Gemini matches responses to the originating function call by
// name.
type googleFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

type googlePart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResponse `json:"functionResponse,omitempty"`
	// ThoughtSignature is Gemini's base64 thought_signature. When thinking mode
	// is on, Gemini emits it as a SIBLING field of the functionCall part (not
	// inside functionCall) and it MUST be echoed back on the same part in the
	// next request (e.g. when re-sending history after a tool result).
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type googleContent struct {
	Role  string       `json:"role"`
	Parts []googlePart `json:"parts"`
}

type googleSystemInstruction struct {
	Parts []googlePart `json:"parts"`
}

type googleFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type googleTool struct {
	FunctionDeclarations []googleFunctionDeclaration `json:"functionDeclarations,omitempty"`
}

type googleThinkingConfig struct {
	ThinkingBudget int `json:"thinkingBudget,omitempty"`
}

type googleGenerationConfig struct {
	ThinkingConfig *googleThinkingConfig `json:"thinkingConfig,omitempty"`
}

type googleRequest struct {
	Contents          []googleContent          `json:"contents"`
	SystemInstruction *googleSystemInstruction `json:"system_instruction,omitempty"`
	Tools             []googleTool             `json:"tools,omitempty"`
	GenerationConfig  *googleGenerationConfig  `json:"generationConfig,omitempty"`
}

type googleCandidate struct {
	Content struct {
		Parts []googlePart `json:"parts"`
		Role  string       `json:"role"`
	} `json:"content"`
	FinishReason string `json:"finishReason,omitempty"`
}

type googleUsageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

type googleResponse struct {
	Candidates    []googleCandidate    `json:"candidates,omitempty"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata,omitempty"`
	Error         *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

func (p *GoogleProvider) Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := p.resolveModel(req)

	var gContents []googleContent
	var systemParts []googlePart
	if req.System != "" {
		systemParts = append(systemParts, googlePart{Text: req.System})
	}

	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			if m.Content != "" {
				systemParts = append(systemParts, googlePart{Text: m.Content})
			}
			continue

		case RoleAssistant:
			role := "model"
			parts := []googlePart{}
			if m.Reasoning != "" {
				parts = append(parts, googlePart{
					Text:    m.Reasoning,
					Thought: true,
				})
			}
			if m.Content != "" {
				parts = append(parts, googlePart{Text: m.Content})
			}
			for _, tc := range m.ToolCalls {
				args := json.RawMessage(tc.Arguments)
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				parts = append(parts, googlePart{
					FunctionCall: &googleFunctionCall{
						Name: tc.Name,
						Args: args,
					},
					// Echo the thought_signature back as a sibling part field,
					// exactly where Gemini emitted it (required when thinking
					// mode is enabled).
					ThoughtSignature: tc.ThoughtSignature,
				})
			}
			if len(parts) > 0 {
				gContents = append(gContents, googleContent{Role: role, Parts: parts})
			}
			continue

		case "tool":
			// A tool result. Gemini expects these as a user-role part carrying a
			// functionResponse matched by name. Ensure we fallback to ToolCallID or Name correctly.

			parts := []googlePart{{
				FunctionResponse: &googleFunctionResponse{
					Name:     m.ToolCallID,
					Response: toolResultAsJSON(m.Content),
				},
			}}
			gContents = append(gContents, googleContent{Role: "user", Parts: parts})
			continue

		default: // RoleUser and any fallback
			if m.Content != "" {
				gContents = append(gContents, googleContent{
					Role:  "user",
					Parts: []googlePart{{Text: m.Content}},
				})
			}
		}
	}

	// Gemini has no concept of a lone system message inside contents; all system
	// instructions are gathered into the top-level system_instruction field.
	gReq := googleRequest{Contents: gContents}
	if len(systemParts) > 0 {
		gReq.SystemInstruction = &googleSystemInstruction{Parts: systemParts}
	}

	// Function declarations mirror the tools the server exposes. Gemini
	// parameters are the raw JSON schema, exactly like OpenAI's.
	var tools []googleTool
	if len(req.Tools) > 0 {
		decls := make([]googleFunctionDeclaration, 0, len(req.Tools))
		for _, t := range req.Tools {
			decls = append(decls, googleFunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			})
		}
		tools = append(tools, googleTool{FunctionDeclarations: decls})
	}
	if len(tools) > 0 {
		gReq.Tools = tools
	}

	// Thinking mode maps onto Gemini's thinkingConfig.thinkingBudget:
	//   - thinking off → budget 0 (disables the model's reasoning stage)
	//   - thinking on  → a positive budget (default DefaultGoogleThinkingBudget)
	//     allocating that many tokens to the reasoning stage. A per-request
	//     reasoning effort override that parses as a number wins over the
	//     provider's configured budget.
	gReq.GenerationConfig = &googleGenerationConfig{
		ThinkingConfig: &googleThinkingConfig{
			ThinkingBudget: p.effectiveThinkingBudget(req),
		},
	}

	body, err := json.Marshal(gReq)
	if err != nil {
		return nil, err
	}

	logger.Tracef("Google request reasoning: thinking=%v thinking_budget=%d",
		p.effectiveThinking(req), gReq.GenerationConfig.ThinkingConfig.ThinkingBudget)
	logger.Tracef("Google request model=%s contents=%d tools=%d thinkingBudget=%d",
		model, len(gContents), len(tools), gReq.GenerationConfig.ThinkingConfig.ThinkingBudget)

	if gReq.SystemInstruction != nil {
		logger.Tracef("Google system instruction parts: %d", len(gReq.SystemInstruction.Parts))
		for _, sp := range gReq.SystemInstruction.Parts {
			logger.Tracef("  system part text=%q", sp.Text)
		}
	}

	logger.Tracef("Google request contents (history):")
	for _, c := range gContents {
		logger.Tracef("  role=%s parts=%d", c.Role, len(c.Parts))
		for _, part := range c.Parts {
			switch {
			case part.FunctionCall != nil:
				logger.Tracef("    function_call name=%s args=%s thought_signature=%s",
					part.FunctionCall.Name, string(part.FunctionCall.Args), part.ThoughtSignature)
			case part.FunctionResponse != nil:
				logger.Tracef("    function_response name=%s response=%s",
					part.FunctionResponse.Name, string(part.FunctionResponse.Response))
			case part.Thought:
				logger.Tracef("    thought text=%q", part.Text)
			case part.Text != "":
				logger.Tracef("    text=%q", part.Text)
			}
		}
	}

	if len(gReq.Tools) > 0 {
		logger.Tracef("Google request tools: %d", len(gReq.Tools))
		for _, t := range gReq.Tools {
			for _, decl := range t.FunctionDeclarations {
				logger.Tracef("  tool name=%s description=%q", decl.Name, decl.Description)
			}
		}
	}

	url := fmt.Sprintf("%s/models/%s:streamGenerateContent?alt=sse&key=%s", p.endpoint, model, p.apiKey)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		logger.Errorf("google request failed: %v", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		logger.Errorf("google HTTP %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var usage *Usage
	stopReason := "STOP"
	var toolCalls []ToolCall

	scanner := bufio.NewScanner(resp.Body)
	// Default scanner buffer handles typical SSE lines; grow it to accommodate
	// large function-call payloads or long text chunks.
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			break
		}

		var chunk googleResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			// ignore malformed line
			logger.Warningf("google: failed to unmarshal chunk: %v", err)
			continue
		}

		if chunk.Error != nil {
			return nil, fmt.Errorf("google api error: %s", chunk.Error.Message)
		}

		if chunk.UsageMetadata != nil {
			usage = &Usage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
			}
		}

		for _, cand := range chunk.Candidates {
			if cand.FinishReason != "" {
				stopReason = cand.FinishReason
			}
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					// Gemini matches tool results by name, so the call id IS the
					// function name. The server's pending-tool bookkeeping and
					// the history's tool-result matching both key off this id.
					toolCalls = append(toolCalls, ToolCall{
						ID:        part.FunctionCall.Name,
						Name:      part.FunctionCall.Name,
						Arguments: string(part.FunctionCall.Args),
						// The thought_signature arrives as a sibling field of
						// the part, not inside the functionCall object.
						ThoughtSignature: part.ThoughtSignature,
					})
				case part.Thought:
					if part.Text != "" {
						fullReasoning.WriteString(part.Text)
						if req.OnReasoning != nil {
							req.OnReasoning(part.Text)
						}
					}
				case part.Text != "":
					fullContent.WriteString(part.Text)
					if onChunk != nil {
						onChunk(part.Text)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
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

// toolResultAsJSON converts a stored tool result into a JSON object suitable
// for Gemini's functionResponse.response field, which MUST be an object (a
// protobuf Struct). A tool result is stored in the history as raw JSON: text
// output ends up as a JSON *string*, while structured output is already an
// object. A plain string/array/number/bool would be rejected by the API with
// HTTP 400 ("Invalid value at ... function_response.response"), so only an
// object is passed through as-is; anything else is wrapped in {"result": ...}.
func toolResultAsJSON(content string) json.RawMessage {
	var raw json.RawMessage
	if json.Unmarshal([]byte(content), &raw) == nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj != nil {
			return raw
		}
		// The content is valid JSON but not an object (string, number, array,
		// bool, null). Wrap the parsed value so response stays an object.
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"result": raw})
		return wrapped
	}
	// Not valid JSON at all — wrap the raw text in an object.
	wrapped, _ := json.Marshal(map[string]string{"result": content})
	return wrapped
}
