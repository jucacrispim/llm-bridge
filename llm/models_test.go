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

// TestCatalogKnown covers a known provider: it has models, its levels come
// ascending and the budget flag is the one the provider parses.
func TestCatalogKnown(t *testing.T) {
	cases := map[string]struct {
		models int
		levels int
		budget bool
	}{
		"deepseek":  {models: 3, levels: 3, budget: false},
		"google":    {models: 8, levels: 5, budget: true},
		"anthropic": {models: 18, levels: 5, budget: true},
	}
	for name, tc := range cases {
		pm, ok := Catalog(name)
		if !ok {
			t.Fatalf("Catalog(%q) ok = false, want true", name)
		}
		if len(pm.Models) != tc.models {
			t.Errorf("Catalog(%q) has %d models, want %d", name, len(pm.Models), tc.models)
		}
		if len(pm.Levels) != tc.levels {
			t.Errorf("Catalog(%q) has %d levels, want %d", name, len(pm.Levels), tc.levels)
		}
		if pm.Budget != tc.budget {
			t.Errorf("Catalog(%q) budget = %v, want %v", name, pm.Budget, tc.budget)
		}
		prev := -1
		for _, level := range pm.Levels {
			idx := levelIndex(level)
			if idx <= prev {
				t.Errorf("Catalog(%q) levels are not ascending: %v", name, pm.Levels)
			}
			prev = idx
		}
	}
}

// levelIndex is the position of a level on the common scale, for ordering.
func levelIndex(level ReasoningLevel) int {
	for i, l := range allLevels {
		if l == level {
			return i
		}
	}
	return -1
}

func TestCatalogUnknown(t *testing.T) {
	if _, ok := Catalog("fake"); ok {
		t.Fatal("Catalog(fake) ok = true, want false")
	}
}

// TestCatalogHasNoDuplicateModel guards the partition: an id announced twice
// would show up twice in a client's completion.
func TestCatalogHasNoDuplicateModel(t *testing.T) {
	seen := map[string]string{}
	for provider, pm := range providerModels {
		for _, id := range pm.Models {
			if first, ok := seen[id]; ok {
				t.Errorf("model %q is announced by both %s and %s", id, first, provider)
			}
			seen[id] = provider
		}
	}
}

// TestCatalogMatchesContextWindow keeps the two tables in sync: every id the
// bridge reports a window for is announced by a provider, or is an alias of one
// it announces.
func TestCatalogMatchesContextWindow(t *testing.T) {
	announced := map[string]bool{}
	for _, pm := range providerModels {
		for _, id := range pm.Models {
			announced[id] = true
			if window, ok := ContextWindow(id); !ok || window == 0 {
				t.Errorf("announced model %q has no context window", id)
			}
		}
	}
	for id, target := range modelAliases {
		if announced[id] {
			t.Errorf("alias %q should not be announced", id)
		}
		if _, ok := contextWindows[target]; !ok {
			t.Errorf("alias %q points at %q, which has no context window", id, target)
		}
	}
	for id := range contextWindows {
		if !announced[id] && modelAliases[id] == "" {
			t.Errorf("model %q has a context window but no provider announces it", id)
		}
	}
}
