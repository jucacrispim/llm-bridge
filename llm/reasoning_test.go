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

import "testing"

func TestValidReasoningLevel(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", "xhigh", "max"} {
		if !ValidReasoningLevel(level) {
			t.Errorf("ValidReasoningLevel(%q) = false", level)
		}
	}
	// Case matters: the value goes to the provider as it came, and none of
	// them documents an upper case spelling.
	for _, level := range []string{"", "HIGH", "turbo", "0", "minimal"} {
		if ValidReasoningLevel(level) {
			t.Errorf("ValidReasoningLevel(%q) = true", level)
		}
	}
}
