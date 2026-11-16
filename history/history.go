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

// AppendUserWithImages appends a user message with optional images. Only a user
// message carries them.
func AppendUserWithImages(hist *[]llm.Message, content string, images []llm.Image) {
	*hist = append(*hist, llm.Message{Role: llm.RoleUser, Content: content, Images: images})
}

func AppendAssistant(hist *[]llm.Message, resp *llm.ChatResponse) {
	// An assistant message with neither content nor tool_calls gets HTTP 400
	// from the OpenAI-compatible API. A turn cancelled mid-stream can leave one.
	if resp.Content == "" && len(resp.ToolCalls) == 0 {
		return
	}
	// Reasoning goes back to the provider only on a message that made tool
	// calls, where deepseek answers 400 without it. For a plain answer it is
	// dead weight in the history.
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
	complete := map[string]bool{}
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
			for _, tc := range m.ToolCalls {
				complete[tc.ID] = true
			}
		}
		clean = append(clean, m)
	}
	// Drop orphaned tool messages: a "tool" result whose id belongs to no
	// surviving (complete) assistant tool_calls message. The provider rejects
	// such a message (a "tool" message must follow the assistant tool_calls it
	// answers). This arises when a turn is cancelled after some read-only tools
	// were already executed but before the mutating ones were approved: the
	// assistant message is dropped above and its executed results must go with
	// it. A "tool" message without an id is left untouched.
	final := make([]llm.Message, 0, len(clean))
	for _, m := range clean {
		if m.Role == "tool" && m.ToolCallID != "" && !complete[m.ToolCallID] {
			continue
		}
		final = append(final, m)
	}
	*hist = final
}
