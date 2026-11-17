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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// useModels loads raw for the test and restores the embedded data after.
func useModels(t *testing.T, raw string) {
	t.Helper()
	prev := currentModels.Load()
	t.Cleanup(func() { currentModels.Store(prev) })
	if err := LoadModels([]byte(raw)); err != nil {
		t.Fatalf("LoadModels: %v", err)
	}
}

// TestCatalogKnown covers the embedded data: each provider has models, its
// levels come ascending and the budget flag is the one the provider parses.
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
	}
}

func TestCatalogUnknown(t *testing.T) {
	if _, ok := Catalog("fake"); ok {
		t.Fatal("Catalog(fake) ok = true, want false")
	}
}

// TestAliasKeepsWindowButIsNotAnnounced covers the deepseek aliases.
func TestAliasKeepsWindowButIsNotAnnounced(t *testing.T) {
	pm, _ := Catalog("deepseek")
	for _, id := range pm.Models {
		if id == "deepseek-chat" || id == "deepseek-reasoner" {
			t.Errorf("alias %q is announced", id)
		}
	}
	if win, ok := ContextWindow("deepseek-chat"); !ok || win == 0 {
		t.Errorf("ContextWindow(deepseek-chat) = %d, %v, want a window", win, ok)
	}
}

func TestContextWindowLongestPrefixWins(t *testing.T) {
	useModels(t, `{"providers":{"p":{"levels":["low"],"models":[
		{"id":"test-model","window":100},
		{"id":"test-model-pro","window":200}]}}}`)

	got, ok := ContextWindow("test-model-pro-001")
	if !ok || got != 200 {
		t.Errorf("ContextWindow(test-model-pro-001) = %d, %v, want 200 (longest prefix)", got, ok)
	}
}

func TestLoadModelsReplacesEverything(t *testing.T) {
	useModels(t, `{"providers":{"mine":{"levels":["low","high"],"budget":true,
		"models":[{"id":"m1","window":10}]}}}`)

	if _, ok := Catalog("deepseek"); ok {
		t.Error("deepseek is still known after the replacement")
	}
	if _, ok := ContextWindow("claude-opus-5"); ok {
		t.Error("an embedded window survived the replacement")
	}
	pm, ok := Catalog("mine")
	if !ok || len(pm.Models) != 1 || pm.Models[0] != "m1" || !pm.Budget {
		t.Errorf("Catalog(mine) = %+v, %v", pm, ok)
	}
}

func TestLoadModelsInvalid(t *testing.T) {
	cases := map[string]string{
		"bad json":       `{`,
		"no providers":   `{"providers":{}}`,
		"unknown level":  `{"providers":{"p":{"levels":["huge"],"models":[{"id":"a","window":1}]}}}`,
		"level order":    `{"providers":{"p":{"levels":["high","low"],"models":[{"id":"a","window":1}]}}}`,
		"empty id":       `{"providers":{"p":{"models":[{"id":"","window":1}]}}}`,
		"zero window":    `{"providers":{"p":{"models":[{"id":"a","window":0}]}}}`,
		"alias missing":  `{"providers":{"p":{"models":[{"id":"a","window":1,"alias":"b"}]}}}`,
		"alias of alias": `{"providers":{"p":{"models":[{"id":"a","window":1},{"id":"b","window":1,"alias":"a"},{"id":"c","window":1,"alias":"b"}]}}}`,
		"id in two":      `{"providers":{"p":{"models":[{"id":"a","window":1}]},"q":{"models":[{"id":"a","window":1}]}}}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			prev := currentModels.Load()
			if err := LoadModels([]byte(raw)); err == nil {
				currentModels.Store(prev)
				t.Fatal("LoadModels accepted invalid data")
			}
			if currentModels.Load() != prev {
				t.Error("invalid data replaced the current models")
			}
		})
	}
}

func TestLoadUserModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	prev := currentModels.Load()
	t.Cleanup(func() { currentModels.Store(prev) })

	if path, err := LoadUserModels(); path != "" || err != nil {
		t.Fatalf("without a file got %q, %v", path, err)
	}

	cfg := filepath.Join(dir, "llm-bridge")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(cfg, "models.json")

	if err := os.WriteFile(file, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := LoadUserModels()
	if err == nil || path != file || !strings.Contains(err.Error(), file) {
		t.Fatalf("invalid file got %q, %v", path, err)
	}
	if _, ok := Catalog("deepseek"); !ok {
		t.Error("an invalid file replaced the embedded data")
	}

	good := `{"providers":{"mine":{"models":[{"id":"m1","window":10}]}}}`
	if err := os.WriteFile(file, []byte(good), 0o644); err != nil {
		t.Fatal(err)
	}
	if path, err := LoadUserModels(); path != file || err != nil {
		t.Fatalf("valid file got %q, %v", path, err)
	}
	if _, ok := Catalog("mine"); !ok {
		t.Error("the user file was not loaded")
	}
}

func TestLoadUserModelsUnreadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	// A directory where the file should be makes ReadFile fail.
	if err := os.MkdirAll(filepath.Join(dir, "llm-bridge", "models.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadUserModels(); err == nil {
		t.Fatal("want an error for an unreadable file")
	}
}

func TestModelsPathWithoutConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if p := ModelsPath(); p != "" {
		t.Errorf("ModelsPath() = %q, want empty", p)
	}
	if path, err := LoadUserModels(); path != "" || err != nil {
		t.Errorf("LoadUserModels() = %q, %v", path, err)
	}
}
