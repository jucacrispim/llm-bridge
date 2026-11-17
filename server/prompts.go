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
	"embed"
	"os"
	"path/filepath"
	"strings"
)

// System prompts live in prompts/*.md. Lookup rules are in
// docs/source/hacking/providers.rst.
//
//go:embed prompts/*.md
var promptsFS embed.FS

const (
	defaultPromptName = "default"
	// basePlaceholder pulls the default prompt into a model prompt.
	basePlaceholder = "{{base}}"
)

// promptsDir returns the user prompts dir, or "" without a user config dir.
// It is a variable so tests can point it elsewhere.
var promptsDir = func() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "llm-bridge", "prompts")
}

// promptNames lists the prompt names available, user files and embedded ones.
func promptNames() map[string]bool {
	names := map[string]bool{}
	if entries, err := promptsFS.ReadDir("prompts"); err == nil {
		for _, e := range entries {
			names[strings.TrimSuffix(e.Name(), ".md")] = true
		}
	}
	if dir := promptsDir(); dir != "" {
		if entries, err := os.ReadDir(dir); err == nil {
			for _, e := range entries {
				if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
					names[strings.TrimSuffix(e.Name(), ".md")] = true
				}
			}
		}
	}
	return names
}

// readPrompt returns the prompt called name. A user file wins over the
// embedded one. A user file that cannot be read falls back to the embedded.
func readPrompt(name string) (string, bool) {
	if dir := promptsDir(); dir != "" {
		if b, err := os.ReadFile(filepath.Join(dir, name+".md")); err == nil {
			return strings.TrimSpace(string(b)), true
		}
	}
	b, err := promptsFS.ReadFile("prompts/" + name + ".md")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(b)), true
}

// promptNameFor picks the prompt for a model: the longest prompt name that is
// a prefix of the model id, then the provider name, then default. The default
// prompt is never matched by prefix.
func promptNameFor(provider, model string) string {
	names := promptNames()
	best := ""
	for name := range names {
		if name == defaultPromptName {
			continue
		}
		if model != "" && strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best != "" {
		return best
	}
	if provider != "" && provider != defaultPromptName && names[provider] {
		return provider
	}
	return defaultPromptName
}

// modelPrompt returns the system prompt for a provider and model. A model
// prompt may hold {{base}}, which stands for the default prompt. Without it
// the file replaces the default.
func modelPrompt(provider, model string) string {
	base, _ := readPrompt(defaultPromptName)
	name := promptNameFor(provider, model)
	if name == defaultPromptName {
		return base
	}
	text, ok := readPrompt(name)
	if !ok || text == "" {
		return base
	}
	return strings.ReplaceAll(text, basePlaceholder, base)
}
