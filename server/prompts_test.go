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

package server

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-bridge/llm"
)

// userPrompts points the user prompts dir at a temp dir holding files.
func userPrompts(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	prev := promptsDir
	promptsDir = func() string { return dir }
	t.Cleanup(func() { promptsDir = prev })
	return dir
}

func noUserPrompts(t *testing.T) {
	t.Helper()
	prev := promptsDir
	promptsDir = func() string { return "" }
	t.Cleanup(func() { promptsDir = prev })
}

func TestModelPromptDefault(t *testing.T) {
	noUserPrompts(t)
	got := modelPrompt("deepseek", "deepseek-flash")
	if !strings.Contains(got, "do NOT call a tool") {
		t.Errorf("embedded default not used, got %q", got)
	}
}

func TestPromptNameFor(t *testing.T) {
	userPrompts(t, map[string]string{
		"claude":      "c",
		"claude-opus": "o",
		"google":      "g",
		"default":     "d",
	})
	cases := []struct{ provider, model, want string }{
		{"anthropic", "claude-opus-5-5", "claude-opus"},
		{"anthropic", "claude-sonnet-5", "claude"},
		{"google", "gemini-3-flash", "google"},
		{"deepseek", "deepseek-flash", "default"},
		{"deepseek", "", "default"},
		{"", "default-model", "default"},
	}
	for _, tc := range cases {
		if got := promptNameFor(tc.provider, tc.model); got != tc.want {
			t.Errorf("promptNameFor(%q, %q) = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestModelPromptReplacesDefault(t *testing.T) {
	userPrompts(t, map[string]string{"default": "base text", "mine": "only mine\n"})
	if got := modelPrompt("p", "mine-1"); got != "only mine" {
		t.Errorf("got %q, want the model prompt alone", got)
	}
}

func TestModelPromptBasePlaceholder(t *testing.T) {
	userPrompts(t, map[string]string{"default": "base text", "mine": "before\n{{base}}\nafter"})
	want := "before\nbase text\nafter"
	if got := modelPrompt("p", "mine-1"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUserDefaultOverridesEmbedded(t *testing.T) {
	userPrompts(t, map[string]string{"default": "my default"})
	if got := modelPrompt("p", "m"); got != "my default" {
		t.Errorf("got %q, want the user default", got)
	}
}

func TestModelPromptEmptyFileFallsBack(t *testing.T) {
	userPrompts(t, map[string]string{"default": "base text", "mine": "  \n"})
	if got := modelPrompt("p", "mine-1"); got != "base text" {
		t.Errorf("got %q, want the default", got)
	}
}

func TestPromptNamesIgnoresNonMarkdown(t *testing.T) {
	dir := userPrompts(t, map[string]string{"mine": "x"})
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub.md"), 0o755); err != nil {
		t.Fatal(err)
	}
	names := promptNames()
	if !names["mine"] || !names["default"] || names["notes"] || names["sub"] {
		t.Errorf("promptNames() = %v", names)
	}
}

func TestReadPromptUnknown(t *testing.T) {
	noUserPrompts(t)
	if got, ok := readPrompt("no-such-prompt"); ok || got != "" {
		t.Errorf("readPrompt(unknown) = %q, %v, want empty and false", got, ok)
	}
}

func TestPromptsDirDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	want := filepath.Join("/cfg", "llm-bridge", "prompts")
	if got := promptsDir(); got != want {
		t.Errorf("promptsDir() = %q, want %q", got, want)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", "")
	if got := promptsDir(); got != "" {
		t.Errorf("promptsDir() = %q, want empty", got)
	}
}

// TestSystemFollowsModel checks the prompt is resolved on each request, so a
// model override picks another file.
func TestSystemFollowsModel(t *testing.T) {
	userPrompts(t, map[string]string{"default": "base", "other": "other prompt"})
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	handleLine(`{"method":"prompt","params":{"text":"a"}}`, st, &w)
	if fp.lastReq.System != "base" {
		t.Fatalf("System = %q, want base", fp.lastReq.System)
	}
	handleLine(`{"method":"prompt","params":{"text":"b","model":"other-1"}}`, st, &w)
	if fp.lastReq.System != "other prompt" {
		t.Errorf("System = %q, want the other prompt", fp.lastReq.System)
	}
}

func TestSystemOverrideBeatsModelPrompt(t *testing.T) {
	userPrompts(t, map[string]string{"other": "other prompt"})
	fp := &fakeProvider{
		name: "fake",
		resp: &llm.ChatResponse{Content: "ok", StopReason: "END_TURN", Usage: &llm.Usage{}},
	}
	st := &state{provider: fp}
	var w bytes.Buffer
	handleLine(`{"method":"prompt","params":{"text":"a","model":"other-1","system":"mine"}}`, st, &w)
	if fp.lastReq.System != "mine" {
		t.Errorf("System = %q, want the override", fp.lastReq.System)
	}
}
