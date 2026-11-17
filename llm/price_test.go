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
	"math"
	"testing"
	"time"
)

func near(t *testing.T, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("cost is nil, want %v", want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", *got, want)
	}
}

func TestLookupPricePrefixAndAlias(t *testing.T) {
	if p, ok := LookupPrice("claude-opus-5-5-20261101"); !ok || p.Input != 4 {
		t.Errorf("dated opus 5.5 = %+v, %v, want input 4", p, ok)
	}
	if p, ok := LookupPrice("claude-opus-5"); !ok || p.Input != 5 {
		t.Errorf("opus 5 = %+v, %v, want input 5", p, ok)
	}
	chat, ok1 := LookupPrice("deepseek-chat")
	flash, ok2 := LookupPrice("deepseek-flash")
	if !ok1 || !ok2 || chat.Input != flash.Input || chat.OffPeak == nil {
		t.Errorf("alias deepseek-chat = %+v, want the price of deepseek-flash", chat)
	}
	if _, ok := LookupPrice("gemma-4"); ok {
		t.Error("gemma-4 has no price")
	}
	if _, ok := LookupPrice(""); ok {
		t.Error("empty model has no price")
	}
}

func TestLoadModelsPriceInvalid(t *testing.T) {
	cases := map[string]string{
		"negative rate": `{"input":-1}`,
		"negative off":  `{"input":1,"off_peak":{"output":-1}}`,
		"over no limit": `{"input":1,"over":{"above":0,"input":2}}`,
		"negative over": `{"input":1,"over":{"above":10,"input":-2}}`,
	}
	for name, price := range cases {
		t.Run(name, func(t *testing.T) {
			prev := currentModels.Load()
			defer currentModels.Store(prev)
			raw := `{"providers":{"p":{"models":[{"id":"a","window":1,"price":` + price + `}]}}}`
			if err := LoadModels([]byte(raw)); err == nil {
				t.Fatal("LoadModels accepted an invalid price")
			}
		})
	}
}

func TestAliasKeepsItsOwnPrice(t *testing.T) {
	useModels(t, `{"providers":{"p":{"models":[
		{"id":"a","window":1,"price":{"input":1}},
		{"id":"b","window":1,"alias":"a","price":{"input":9}}]}}}`)
	if p, _ := LookupPrice("b"); p.Input != 9 {
		t.Errorf("alias price = %v, want its own 9", p.Input)
	}
}

func TestAnthropicCost(t *testing.T) {
	// opus 5.5: input 4, cached 0.20, write 5, output 20
	u := &anthropicUsage{InputTokens: 1_000_000, CacheReadInputTokens: 1_000_000,
		CacheCreationInputTokens: 1_000_000, OutputTokens: 1_000_000}
	near(t, anthropicCost("claude-opus-5-5", u), 4+0.20+5+20)

	if anthropicCost("no-such-model", u) != nil {
		t.Error("a model without a price must give nil")
	}
}

func TestAnthropicCostWriteDefaultsToInput(t *testing.T) {
	useModels(t, `{"providers":{"p":{"models":[
		{"id":"a","window":1,"price":{"input":3,"output":6}}]}}}`)
	near(t, anthropicCost("a", &anthropicUsage{CacheCreationInputTokens: 1_000_000}), 3)
}

func TestAnthropicCostLongPrompt(t *testing.T) {
	// haiku 5.5: up to 100k is 0.10 in, over it is 0.50. Cache counts in the prompt.
	short := &anthropicUsage{InputTokens: 100_000}
	near(t, anthropicCost("claude-haiku-5-5", short), 0.1*0.10)

	long := &anthropicUsage{InputTokens: 1_000, CacheReadInputTokens: 100_000}
	near(t, anthropicCost("claude-haiku-5-5", long), (1_000*0.50+100_000*0.05)/1e6)
}

func TestGoogleCost(t *testing.T) {
	// 3.8 flash: input 0.75, cached 0.075, output 3.75. Thoughts are output.
	u := &googleUsageMetadata{PromptTokenCount: 2_000_000, CachedContentTokenCount: 1_000_000,
		CandidatesTokenCount: 500_000, ThoughtsTokenCount: 500_000}
	near(t, googleCost("gemini-3.8-flash", u), 0.75+0.075+3.75)

	if googleCost("gemini-2.5-pro", u) != nil {
		t.Error("a model without a price must give nil")
	}
}

func TestGoogleCostLongPrompt(t *testing.T) {
	// 3.1 pro: over 200k the input is 4 and the output 18.
	u := &googleUsageMetadata{PromptTokenCount: 200_001, CandidatesTokenCount: 1_000_000}
	near(t, googleCost("gemini-3.1-pro", u), 200_001*4/1e6+18)

	u = &googleUsageMetadata{PromptTokenCount: 200_000, CandidatesTokenCount: 1_000_000}
	near(t, googleCost("gemini-3.1-pro", u), 200_000*2/1e6+12)
}

func TestDeepseekPeak(t *testing.T) {
	at := func(day, hour int) time.Time { return time.Date(2026, 11, day, hour, 30, 0, 0, time.UTC) }
	// 2026-11-16 is a Monday, 2026-11-21 a Saturday.
	cases := []struct {
		t    time.Time
		peak bool
	}{
		{at(16, 0), false}, {at(16, 1), true}, {at(16, 3), true}, {at(16, 4), false},
		{at(16, 5), false}, {at(16, 6), true}, {at(16, 9), true}, {at(16, 10), false},
		{at(21, 2), false}, {at(21, 7), false},
	}
	for _, tc := range cases {
		if got := deepseekPeak(tc.t); got != tc.peak {
			t.Errorf("deepseekPeak(%v) = %v, want %v", tc.t, got, tc.peak)
		}
	}
}

func TestDeepseekCost(t *testing.T) {
	// flash peak: miss 0.3, hit 0.006, out 1.2. Off-peak is half.
	u := &openAIUsage{PromptTokens: 2_000_000, PromptCacheHitTokens: 1_000_000, CompletionTokens: 1_000_000}
	peak := time.Date(2026, 11, 16, 2, 0, 0, 0, time.UTC)
	off := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	near(t, deepseekCost("deepseek-flash", u, peak), 0.3+0.006+1.2)
	near(t, deepseekCost("deepseek-flash", u, off), 0.15+0.003+0.6)
	near(t, deepseekCost("deepseek-chat", u, off), 0.15+0.003+0.6)

	if deepseekCost("no-such-model", u, peak) != nil {
		t.Error("a model without a price must give nil")
	}
}

func TestDeepseekCostWithoutOffPeakIsFlat(t *testing.T) {
	useModels(t, `{"providers":{"p":{"models":[
		{"id":"a","window":1,"price":{"input":1,"output":2}}]}}}`)
	u := &openAIUsage{PromptTokens: 1_000_000}
	off := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	near(t, deepseekCost("a", u, off), 1)
}
