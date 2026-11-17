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

// ProviderModels is what the bridge knows about a provider: the model ids it
// answers for and the reasoning effort it takes. The ids come from the context
// window table, so a model announced here always reports a window.
type ProviderModels struct {
	// Models are the ids the provider answers for, newest first, without the
	// aliases (see modelAliases).
	Models []string
	// Levels are the reasoning levels the provider accepts, ascending.
	Levels []ReasoningLevel
	// Budget reports whether a token budget works instead of a level.
	Budget bool
}

// modelAliases are ids the providers redirect to another model. They stay in
// the context window table, since a request may still name one, and are left
// out of the announcement: picking an alias is picking its target.
var modelAliases = map[string]string{
	"deepseek-chat":     "deepseek-flash",
	"deepseek-reasoner": "deepseek-flash",
}

var allLevels = []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}

// providerModels partitions the known model ids by provider and records the
// reasoning controls of each one. DeepSeek takes a level and nothing else:
// xhigh and max go out as high, so they are not announced.
var providerModels = map[string]ProviderModels{
	"deepseek": {
		Models: []string{"deepseek-flash", "deepseek-v4-flash", "deep-v4-pro"},
		Levels: []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh},
	},
	"google": {
		Models: []string{"gemini-3.8-flash", "gemini-3.1-pro", "gemini-3-flash",
			"gemini-2.5-pro", "gemini-2.5-flash", "gemini-1.5-pro",
			"gemini-1.5-flash", "gemma-4"},
		Levels: allLevels,
		Budget: true,
	},
	"anthropic": {
		Models: []string{"claude-fable-5-2", "claude-fable-5-1", "claude-fable-5",
			"claude-mythos-5-2", "claude-mythos-5-1", "claude-mythos-5",
			"claude-opus-5-5", "claude-opus-5", "claude-opus-4-8",
			"claude-opus-4-7", "claude-opus-4-6", "claude-opus-4-5",
			"claude-sonnet-5-5", "claude-sonnet-5", "claude-sonnet-4-6",
			"claude-sonnet-4-5", "claude-haiku-5-5", "claude-haiku-4-5"},
		Levels: allLevels,
		Budget: true,
	},
}

// Catalog returns the models and reasoning controls of a provider. ok is false
// for a provider the bridge does not know, which a client still gets announced
// with its name and current model alone.
func Catalog(name string) (ProviderModels, bool) {
	pm, ok := providerModels[name]
	return pm, ok
}
