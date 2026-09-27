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

package history

import (
	"strings"

	"llm-bridge/llm"
)

const (
	ephemeralStart = "<!-- ephemeral -->"
	ephemeralEnd   = "<!-- /ephemeral -->"
)

// MarkEphemeral returns a copy of content tagged as ephemeral.
// The block between the start and end markers will be removed by Sanitize,
// ensuring it is sent to the model only once (in the message it was added).
func MarkEphemeral(content string) string {
	return ephemeralStart + "\n" + content + "\n" + ephemeralEnd
}

// StripEphemeral removes every block marked with <!-- ephemeral --> ... <!-- /ephemeral -->
// from the given content, preserving the surrounding text.
func StripEphemeral(content string) string {
	for {
		start := strings.Index(content, ephemeralStart)
		if start == -1 {
			return content
		}
		afterStart := content[start+len(ephemeralStart):]
		end := strings.Index(afterStart, ephemeralEnd)
		if end == -1 {
			// malformed block, remove the start marker anyway
			content = content[:start] + afterStart
			continue
		}
		endAbs := start + len(ephemeralStart) + end + len(ephemeralEnd)
		content = content[:start] + content[endAbs:]
	}
}

func AppendUser(hist *[]llm.Message, content string) {
	AppendUserWithImages(hist, content, nil)
}

// AppendUserWithImages appends a user message carrying optional image
// attachments. Images are only meaningful on user messages (both DeepSeek and
// Gemini reject images elsewhere).
func AppendUserWithImages(hist *[]llm.Message, content string, images []llm.Image) {
	*hist = append(*hist, llm.Message{Role: llm.RoleUser, Content: content, Images: images})
}

func AppendAssistant(hist *[]llm.Message, resp *llm.ChatResponse) {
	// An assistant message must carry either content or tool_calls; the
	// OpenAI-compatible API rejects an empty one (both unset) with HTTP 400.
	// A partial turn cancelled mid-stream can leave a ChatResponse with only
	// reasoning and no content, and that reasoning is stripped below for
	// messages without tool calls — so dropping it here prevents an empty
	// assistant message from ever reaching the history.
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return
	}
	// Reasoning (chain-of-thought) is never sent back to a provider for an
	// assistant message without tool calls: DeepSeek omits reasoning_content
	// for those (only re-sends it when the message performed tool calls, to
	// satisfy a 400), and Gemini has no input channel for reasoning at all.
	// Storing it here would be dead weight in the bridge's history, so it is
	// kept only when the message actually carried tool calls.
	reasoning := resp.Reasoning
	if len(resp.ToolCalls) == 0 {
		reasoning = ""
	}
	*hist = append(*hist, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
		Reasoning: reasoning,
	})
}

func AppendToolResult(hist *[]llm.Message, id, content string) {
	*hist = append(*hist, llm.Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: id,
	})
}

func Sanitize(hist *[]llm.Message) {
	out := make([]llm.Message, 0, len(*hist))
	for _, m := range *hist {
		m.Content = StripEphemeral(m.Content)
		out = append(out, m)
	}
	*hist = out

	answered := map[string]bool{}
	for _, m := range *hist {
		if m.Role == "tool" && m.ToolCallID != "" {
			answered[m.ToolCallID] = true
		}
	}
	clean := make([]llm.Message, 0, len(*hist))
	for _, m := range *hist {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 {
			allAnswered := true
			for _, tc := range m.ToolCalls {
				if !answered[tc.ID] {
					allAnswered = false
					break
				}
			}
			if !allAnswered {
				continue
			}
		}
		clean = append(clean, m)
	}
	*hist = clean
}
