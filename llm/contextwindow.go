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

// ContextWindow returns the token window of a model and whether it is known:
// exact match first, then the longest matching prefix, so a dated snapshot
// matches its family. The providers do not expose the window over the API, so
// it comes from models.json. An unknown model gives (0, false), so a caller
// reports a null context_pct instead of a wrong window.
func ContextWindow(model string) (int, bool) {
	if model == "" {
		return 0, false
	}
	windows := currentModels.Load().windows
	if win, ok := windows[model]; ok {
		return win, true
	}
	best := ""
	for prefix := range windows {
		if strings.HasPrefix(model, prefix) && len(prefix) > len(best) {
			best = prefix
		}
	}
	if best == "" {
		return 0, false
	}
	return windows[best], true
}
