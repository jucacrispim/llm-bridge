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
	*hist = append(*hist, llm.Message{Role: llm.RoleUser, Content: content})
}

func AppendAssistant(hist *[]llm.Message, resp *llm.ChatResponse) {
	*hist = append(*hist, llm.Message{
		Role:      llm.RoleAssistant,
		Content:   resp.Content,
		ToolCalls: resp.ToolCalls,
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
