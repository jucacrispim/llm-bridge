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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"llm-bridge/logger"
)

// DefaultGoogleThinkingBudget is the thinkingBudget sent when thinking is on.
// 0 disables thinking; a positive value gives the reasoning stage that many
// tokens.
const DefaultGoogleThinkingBudget = 1024

// DefaultGoogleModel is used when no explicit model is configured.
const DefaultGoogleModel = "gemini-3-flash"

type GoogleProvider struct {
	apiKey   string
	endpoint string
	model    string
	thinking bool
	// thinkingBudget is the thinkingBudget sent when thinking is on. 0 disables
	// thinking.
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

// GoogleThinkingFromEnv reads GOOGLE_THINKING, returning def when unset.
func GoogleThinkingFromEnv(def bool) bool {
	return parseBoolEnv("GOOGLE_THINKING", def)
}

// NewGoogleProviderFromEnv creates a provider reading from environment variables:
//
//	GOOGLE_API_KEY         (required; GEMINI_API_KEY also honored)
//	GOOGLE_URL             (optional, default https://generativelanguage.googleapis.com/v1beta)
//	GOOGLE_MODEL           (optional, default gemini-3-flash; GEMINI_MODEL also honored)
//	GOOGLE_THINKING        (optional, default true, thinking mode)
//	GOOGLE_THINKING_BUDGET (optional, default 1024, thinkingConfig.thinkingBudget)
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

// googleCost prices a call, or returns nil for a model without a price.
// Gemini bills the thinking tokens as output, and they are not in the
// candidates count. The prompt count already holds the cached tokens.
func googleCost(model string, u *googleUsageMetadata) *float64 {
	p, ok := LookupPrice(model)
	if !ok {
		return nil
	}
	r := p.RatesFor(u.PromptTokenCount)
	miss := u.PromptTokenCount - u.CachedContentTokenCount
	out := u.CandidatesTokenCount + u.ThoughtsTokenCount
	cost := (float64(miss)*r.Input +
		float64(u.CachedContentTokenCount)*r.Cached +
		float64(out)*r.Output) / 1e6
	return &cost
}

// Thinking reports whether thinking mode is enabled for this provider.
func (p *GoogleProvider) Thinking() bool { return p.thinking }

// SetThinking updates the thinking mode of the provider.
func (p *GoogleProvider) SetThinking(thinking bool) { p.thinking = thinking }

// ThinkingBudget returns the thinkingBudget used when thinking is on.
func (p *GoogleProvider) ThinkingBudget() int { return p.thinkingBudget }

// SetThinkingBudget overrides the thinkingBudget. 0 disables thinking.
func (p *GoogleProvider) SetThinkingBudget(budget int) { p.thinkingBudget = budget }

// effectiveThinking returns the thinking mode for a request: the per-request
// override, or the configured one.
func (p *GoogleProvider) effectiveThinking(req ChatRequest) bool {
	if req.Thinking != nil {
		return *req.Thinking
	}
	return p.thinking
}

// resolveModel returns the model for a request: the per-request model or the
// configured one.
func (p *GoogleProvider) resolveModel(req ChatRequest) string {
	if req.Model != "" {
		return req.Model
	}
	return p.model
}

// ParseBudget parses a non-negative integer, returning def when s is not one.
func ParseBudget(s string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n >= 0 {
		return n
	}
	return def
}

// googleLevelBudgets maps a reasoning level onto a thinkingBudget. Gemini takes
// a number, so a level has to become one; the values are the ones the Gemini
// docs pair with each effort.
var googleLevelBudgets = map[ReasoningLevel]int{
	ReasoningLow:    1024,
	ReasoningMedium: 4096,
	ReasoningHigh:   8192,
	ReasoningXHigh:  16384,
	ReasoningMax:    32768,
}

// effectiveThinkingBudget returns the thinkingBudget for a request: the
// per-request numeric budget, then the per-request level, then the configured
// budget. Thinking off always gives 0.
func (p *GoogleProvider) effectiveThinkingBudget(req ChatRequest) int {
	if !p.effectiveThinking(req) {
		return 0
	}
	if req.ThinkingBudget > 0 {
		return req.ThinkingBudget
	}
	if req.ReasoningLevel != nil {
		if budget, ok := googleLevelBudgets[*req.ReasoningLevel]; ok {
			return budget
		}
	}
	return p.thinkingBudget
}

// googleFunctionCall is Gemini's function-call part payload: args is an object,
// unlike OpenAI's JSON string. The thought_signature is a sibling field of the
// part (googlePart.ThoughtSignature), not a field here.
type googleFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

// googleFunctionResponse is a tool result as a user-role part. Gemini matches
// it to the call by name.
type googleFunctionResponse struct {
	Name     string          `json:"name"`
	Response json.RawMessage `json:"response"`
}

// googleInlineData carries an image inline as base64. Gemini has no OpenAI
// image_url shape.
type googleInlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

// googleFileData references a file by URI (a Files API uri or a public URL).
type googleFileData struct {
	MIMEType string `json:"mimeType,omitempty"`
	FileURI  string `json:"fileUri"`
}

type googlePart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	FunctionCall     *googleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *googleFunctionResponse `json:"functionResponse,omitempty"`
	// InlineData carries an image as base64; FileData references one by URI.
	// They are the two ways Gemini takes an image.
	InlineData *googleInlineData `json:"inlineData,omitempty"`
	FileData   *googleFileData   `json:"fileData,omitempty"`
	// ThoughtSignature is Gemini's base64 thought_signature. It is a sibling of
	// the functionCall part and must be echoed back on the same part when the
	// history is re-sent, or the API answers 400.
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
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount"`
}

type googleResponse struct {
	Candidates    []googleCandidate    `json:"candidates,omitempty"`
	UsageMetadata *googleUsageMetadata `json:"usageMetadata,omitempty"`
	Error         *struct {
		Message string `json:"message"`
		Code    int    `json:"code"`
	} `json:"error,omitempty"`
}

// googleStopReason maps finishReason onto the common vocabulary. The remaining
// values (SAFETY, RECITATION and friends) are passed through, because a client
// wants to know which one it was.
func googleStopReason(reason string) string {
	switch reason {
	case "STOP":
		return StopEndTurn
	case "MAX_TOKENS":
		return StopMaxTokens
	}
	return reason
}

func (p *GoogleProvider) Chat(ctx context.Context, req ChatRequest, onChunk func(string)) (*ChatResponse, error) {
	model := p.resolveModel(req)

	gReq := p.buildGoogleRequest(req)

	body, err := json.Marshal(gReq)
	if err != nil {
		return nil, err
	}

	logger.Tracef("Google request reasoning: thinking=%v thinking_budget=%d",
		p.effectiveThinking(req), gReq.GenerationConfig.ThinkingConfig.ThinkingBudget)
	logger.Tracef("Google request model=%s contents=%d tools=%d thinkingBudget=%d",
		model, len(gReq.Contents), len(gReq.Tools), gReq.GenerationConfig.ThinkingConfig.ThinkingBudget)

	if gReq.SystemInstruction != nil {
		logger.Tracef("Google system instruction parts: %d", len(gReq.SystemInstruction.Parts))
		for _, sp := range gReq.SystemInstruction.Parts {
			logger.Tracef("  system part text=%q", sp.Text)
		}
	}

	logger.Tracef("Google request contents (history):")
	for _, c := range gReq.Contents {
		logger.Tracef("  role=%s parts=%d", c.Role, len(c.Parts))
		for _, part := range c.Parts {
			switch {
			case part.FunctionCall != nil:
				logger.Tracef("    function_call name=%s args=%s thought_signature=%s",
					part.FunctionCall.Name, string(part.FunctionCall.Args), part.ThoughtSignature)
			case part.FunctionResponse != nil:
				logger.Tracef("    function_response name=%s response=%s",
					part.FunctionResponse.Name, string(part.FunctionResponse.Response))
			case part.InlineData != nil:
				logger.Tracef("    inline_data mime_type=%s bytes=%d", part.InlineData.MIMEType, len(part.InlineData.Data))
			case part.FileData != nil:
				logger.Tracef("    file_data mime_type=%s file_uri=%s", part.FileData.MIMEType, part.FileData.FileURI)
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

	var resp *http.Response
	for attempt := 0; ; attempt++ {
		resp, err = p.doRequest(ctx, url, body)
		if err != nil {
			logger.Errorf("google request failed: %v", err)
			return nil, err
		}
		if resp.StatusCode == http.StatusOK {
			break
		}
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		// Gemini may reject an external fileUri with 400. Fall back once:
		// download the URL images and embed them inline, then retry.
		if attempt == 0 && resp.StatusCode == http.StatusBadRequest {
			if inlined, ok := inlineImageURLs(req.Messages); ok {
				req.Messages = inlined
				gReq = p.buildGoogleRequest(req)
				body, err = json.Marshal(gReq)
				if err != nil {
					return nil, err
				}
				continue
			}
		}
		logger.Errorf("google HTTP %d: %s", resp.StatusCode, string(respBody))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	defer resp.Body.Close()

	var fullContent strings.Builder
	var fullReasoning strings.Builder
	var usage *Usage
	stopReason := StopEndTurn
	var toolCalls []ToolCall

	scanner := bufio.NewScanner(resp.Body)
	// Raised above the default so a large function-call payload fits.
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
			cached := chunk.UsageMetadata.CachedContentTokenCount
			usage = &Usage{
				PromptTokens:     chunk.UsageMetadata.PromptTokenCount,
				CompletionTokens: chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:      chunk.UsageMetadata.TotalTokenCount,
				CachedTokens:     cached,
				CostUSD:          googleCost(model, chunk.UsageMetadata),
			}
		}

		for _, cand := range chunk.Candidates {
			if cand.FinishReason != "" {
				stopReason = googleStopReason(cand.FinishReason)
			}
			for _, part := range cand.Content.Parts {
				switch {
				case part.FunctionCall != nil:
					// Gemini matches a tool result by name, so the call id is the
					// function name. The pending-tool bookkeeping and the history
					// both key off it.
					toolCalls = append(toolCalls, ToolCall{
						ID:        part.FunctionCall.Name,
						Name:      part.FunctionCall.Name,
						Arguments: string(part.FunctionCall.Args),
						// A sibling field of the part, not inside functionCall.
						Signature: part.ThoughtSignature,
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

// toolResultAsJSON converts a tool result into a JSON object for Gemini's
// functionResponse.response, which must be an object (a protobuf Struct). A
// string, array, number or bool is wrapped in {"result": ...}; the API answers
// 400 otherwise.
func toolResultAsJSON(content string) json.RawMessage {
	var raw json.RawMessage
	if json.Unmarshal([]byte(content), &raw) == nil {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) == nil && obj != nil {
			return raw
		}
		// Valid JSON, but not an object.
		wrapped, _ := json.Marshal(map[string]json.RawMessage{"result": raw})
		return wrapped
	}
	// Not valid JSON at all: wrap the raw text.
	wrapped, _ := json.Marshal(map[string]string{"result": content})
	return wrapped
}

// buildGoogleRequest converts a ChatRequest into the Gemini wire request:
// contents, the top-level system instruction, the function declarations and the
// thinking budget. It runs again with inlined images when Gemini rejects a URL
// image.
func (p *GoogleProvider) buildGoogleRequest(req ChatRequest) googleRequest {
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
					// Sibling of the functionCall, where Gemini emitted it.
					ThoughtSignature: tc.Signature,
				})
			}
			if len(parts) > 0 {
				gContents = append(gContents, googleContent{Role: role, Parts: parts})
			}
			continue

		case "tool":
			// A tool result goes back as a user-role functionResponse, matched by
			// name.
			parts := []googlePart{{
				FunctionResponse: &googleFunctionResponse{
					Name:     m.ToolCallID,
					Response: toolResultAsJSON(m.Content),
				},
			}}
			gContents = append(gContents, googleContent{Role: "user", Parts: parts})
			continue

		default: // RoleUser and any fallback
			parts := []googlePart{}
			if m.Content != "" {
				parts = append(parts, googlePart{Text: m.Content})
			}
			// No image_url in Gemini: an inline image becomes an inlineData
			// part, an external URL becomes a fileData part.
			for _, im := range m.Images {
				if im.URL != "" {
					parts = append(parts, googlePart{FileData: &googleFileData{
						MIMEType: im.MIME,
						FileURI:  im.URL,
					}})
				} else {
					parts = append(parts, googlePart{InlineData: &googleInlineData{
						MIMEType: im.MIME,
						Data:     im.Data,
					}})
				}
			}
			if len(parts) > 0 {
				gContents = append(gContents, googleContent{Role: "user", Parts: parts})
			}
		}
	}

	// Gemini has no lone system message inside contents, so every system text
	// goes to the top-level system_instruction.
	gReq := googleRequest{Contents: gContents}
	if len(systemParts) > 0 {
		gReq.SystemInstruction = &googleSystemInstruction{Parts: systemParts}
	}

	// Function declarations mirror the tools the server exposes, with the same
	// JSON schema parameters OpenAI gets.
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

	// Thinking maps onto thinkingBudget: 0 when off, a positive budget when on,
	// and a numeric per-request effort wins over the configured budget.
	gReq.GenerationConfig = &googleGenerationConfig{
		ThinkingConfig: &googleThinkingConfig{
			ThinkingBudget: p.effectiveThinkingBudget(req),
		},
	}
	return gReq
}

// doRequest sends a pre-marshalled body to the Gemini streaming endpoint.
func (p *GoogleProvider) doRequest(ctx context.Context, url string, body []byte) (*http.Response, error) {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	return p.client.Do(httpReq)
}

// inlineImageURLs returns a copy of messages with every URL image downloaded and
// embedded inline. It is the fallback for a fileUri Gemini rejected; the second
// result is false when there was no URL image or a download failed.
func inlineImageURLs(messages []Message) ([]Message, bool) {
	found := false
	out := make([]Message, len(messages))
	copy(out, messages)
	for i := range out {
		if len(out[i].Images) == 0 {
			continue
		}
		imgs := make([]Image, len(out[i].Images))
		copy(imgs, out[i].Images)
		for j := range imgs {
			if imgs[j].URL == "" {
				continue
			}
			found = true
			raw, err := downloadImage(imgs[j].URL)
			if err != nil {
				logger.Warningf("google: inline fallback failed for %s: %v", imgs[j].URL, err)
				return nil, false
			}
			mime := detectImageMIME(raw)
			if mime == "" {
				mime = imgs[j].MIME
			}
			imgs[j] = Image{
				MIME: mime,
				Data: base64.StdEncoding.EncodeToString(raw),
			}
		}
		out[i].Images = imgs
	}
	return out, found
}

// downloadImage fetches an http(s) image, capped at 64 MiB.
func downloadImage(url string) ([]byte, error) {
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}
