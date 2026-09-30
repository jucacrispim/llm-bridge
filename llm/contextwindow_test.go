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

func TestContextWindowExact(t *testing.T) {
	cases := []struct {
		model string
		want  int
	}{
		// DeepSeek: both current API models have a 1M window; chat/reasoner
		// are aliases redirected to deepseek-flash.
		{"deepseek-flash", 1_000_000},
		{"deepseek-chat", 1_000_000},
		{"deepseek-reasoner", 1_000_000},
		{"deepseek-v4-flash", 1_000_000},
		{"deep-v4-pro", 1_000_000},
		// Google Gemini.
		{"gemini-3.8-flash", 1_000_000},
		{"gemini-3.1-pro", 1_000_000},
		{"gemini-3-flash", 200_000},
		{"gemini-2.5-pro", 1_000_000},
		{"gemini-2.5-flash", 1_000_000},
		{"gemini-1.5-pro", 2_000_000},
		{"gemini-1.5-flash", 1_000_000},
		// Gemma (open models): conservative lower bound of the 128K..256K range.
		{"gemma-4", 128_000},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, ok := ContextWindow(tc.model)
			if !ok {
				t.Fatalf("ContextWindow(%q) ok = false, want true", tc.model)
			}
			if got != tc.want {
				t.Errorf("ContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}

func TestContextWindowPrefix(t *testing.T) {
	// Providers often return versioned ids (a base name plus a suffix); the
	// lookup must fall back to the longest matching prefix.
	cases := []struct {
		model string
		want  int
	}{
		{"gemini-2.5-flash-001", 1_000_000},
		{"gemini-1.5-pro-latest", 2_000_000},
		{"deepseek-v4-flash-preview", 1_000_000},
		{"gemma-4-27b", 128_000},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			got, ok := ContextWindow(tc.model)
			if !ok {
				t.Fatalf("ContextWindow(%q) ok = false, want true", tc.model)
			}
			if got != tc.want {
				t.Errorf("ContextWindow(%q) = %d, want %d", tc.model, got, tc.want)
			}
		})
	}
}

func TestContextWindowUnknown(t *testing.T) {
	cases := []struct {
		name  string
		model string
	}{
		{"empty", ""},
		{"unrelated", "gpt-4o"},
		// "gemini-3" is not itself a key and does not prefix any known id
		// (the known ids are gemini-3.8-flash / gemini-3.1-pro / gemini-3-flash,
		// none of which starts with "gemini-3" followed by nothing useful).
		{"partial", "gemini-2.0-flash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ContextWindow(tc.model)
			if ok {
				t.Errorf("ContextWindow(%q) ok = true, want false", tc.model)
			}
			if got != 0 {
				t.Errorf("ContextWindow(%q) = %d, want 0 for unknown", tc.model, got)
			}
		})
	}
}

func TestContextWindowLongestPrefixWins(t *testing.T) {
	// Guard the "longest matching prefix" rule directly: temporarily add a
	// short and a long key sharing a prefix, and assert the longer one wins
	// for an id that matches both.
	const short, long = "test-model", "test-model-pro"
	contextWindows[short] = 100
	contextWindows[long] = 200
	t.Cleanup(func() {
		delete(contextWindows, short)
		delete(contextWindows, long)
	})

	got, ok := ContextWindow("test-model-pro-001")
	if !ok {
		t.Fatal("ContextWindow(test-model-pro-001) ok = false, want true")
	}
	if got != 200 {
		t.Errorf("ContextWindow(test-model-pro-001) = %d, want 200 (longest prefix)", got)
	}
}
