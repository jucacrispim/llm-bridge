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

// contextWindows maps a model id (or a leading prefix of one) to its token
// context window. Providers do not expose this over the API, so the values are
// static. Model ids often carry suffixes (e.g. gemini-2.5-flash-001) or alias
// names, so lookup is exact first and then by longest matching prefix.
//
// Keep the values in sync with the docs (docs/source/hacking/providers.rst) and with
// the actual provider catalog; when in doubt prefer the smaller window.
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

// ContextWindow returns the token context window for the given model id, and
// whether the model is known. Lookup is exact first, then by the longest
// matching known prefix (models commonly carry version suffixes like -001).
// Unknown models return (0, false) so callers can report a null context
// percentage instead of guessing a wrong window.
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
