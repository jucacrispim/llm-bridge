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

import "strings"

// contextWindows maps a model id, or a prefix of one, to its token window. The
// providers do not expose it over the API, so the values are static. Keep them
// in sync with docs/source/hacking/providers.rst; when in doubt prefer the
// smaller window.
var contextWindows = map[string]int{
	// DeepSeek: both current API models have a 1M window. deepseek-chat and
	// deepseek-reasoner are aliases redirected to deepseek-flash.
	"deepseek-flash":    1_000_000,
	"deepseek-chat":     1_000_000,
	"deepseek-reasoner": 1_000_000,
	"deepseek-v4-flash": 1_000_000,
	"deep-v4-pro":       1_000_000,

	// Google Gemini.
	"gemini-3.8-flash": 1_000_000,
	"gemini-3.1-pro":   1_000_000,
	"gemini-3-flash":   200_000,
	"gemini-2.5-pro":   1_000_000,
	"gemini-2.5-flash": 1_000_000,
	"gemini-1.5-pro":   2_000_000,
	"gemini-1.5-flash": 1_000_000,

	// Gemma (open models): 128K..256K depending on the variant; take the
	// conservative lower bound.
	"gemma-4": 128_000,
}

// ContextWindow returns the token window of a model and whether it is known:
// exact match first, then the longest matching prefix. An unknown model gives
// (0, false), so a caller reports a null context_pct instead of a wrong window.
func ContextWindow(model string) (int, bool) {
	if model == "" {
		return 0, false
	}
	if win, ok := contextWindows[model]; ok {
		return win, true
	}
	best := ""
	for prefix := range contextWindows {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return 0, false
	}
	return contextWindows[best], true
}
