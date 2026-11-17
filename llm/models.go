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
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
)

// Model data lives in models.json. The format is described in
// docs/source/hacking/providers.rst.
//
//go:embed models.json
var embeddedModels []byte

// modelsFileName is the name of the user override, looked up in the user
// config dir under llm-bridge.
const modelsFileName = "models.json"

// ProviderModels is what the bridge knows about a provider: the model ids it
// answers for and the reasoning effort it takes.
type ProviderModels struct {
	// Models are the ids the provider answers for, in file order, without the
	// aliases.
	Models []string
	// Levels are the reasoning levels the provider accepts, ascending.
	Levels []ReasoningLevel
	// Budget reports whether a token budget works instead of a level.
	Budget bool
}

// Rates is what a model charges, in USD per million tokens. Input is the
// uncached prompt, Cached is a prompt token read from the cache and Output
// includes the reasoning. CacheWrite is optional: without it a cache write is
// billed as Input. A rate left out is zero.
type Rates struct {
	Input      float64 `json:"input"`
	Cached     float64 `json:"cached"`
	CacheWrite float64 `json:"cache_write"`
	Output     float64 `json:"output"`
}

// OverRates replace the base rates for a prompt above Above tokens.
type OverRates struct {
	Above int `json:"above"`
	Rates
}

// Price is the price of a model. Each provider turns it into a cost with its
// own rules, so the shape is just the data.
type Price struct {
	Rates
	// Over is the tier for long prompts, for the models that have one.
	Over *OverRates `json:"over,omitempty"`
	// OffPeak is the rate outside the peak hours, for the models that have one.
	// The base rates are then the peak ones.
	OffPeak *Rates `json:"off_peak,omitempty"`
}

// RatesFor returns the base rates, or the long prompt tier when the prompt is
// above its limit.
func (p Price) RatesFor(promptTokens int) Rates {
	if p.Over != nil && promptTokens > p.Over.Above {
		return p.Over.Rates
	}
	return p.Rates
}

func (r Rates) valid() bool {
	return r.Input >= 0 && r.Cached >= 0 && r.CacheWrite >= 0 && r.Output >= 0
}

type modelsFile struct {
	Providers map[string]providerFile `json:"providers"`
}

type providerFile struct {
	Levels []ReasoningLevel `json:"levels"`
	Budget bool             `json:"budget"`
	Models []modelFile      `json:"models"`
}

type modelFile struct {
	ID     string `json:"id"`
	Window int    `json:"window"`
	// Alias names the model this id redirects to. An alias keeps its window,
	// since a request may still name it, and is not announced.
	Alias string `json:"alias,omitempty"`
	// Price is optional. An alias without one takes the price of its target.
	Price *Price `json:"price,omitempty"`
}

// modelData is the parsed form of a models file.
type modelData struct {
	providers map[string]ProviderModels
	windows   map[string]int
	prices    map[string]Price
}

var currentModels atomic.Pointer[modelData]

func init() {
	data, err := parseModels(embeddedModels)
	if err != nil {
		panic("llm: embedded models.json: " + err.Error())
	}
	currentModels.Store(data)
}

// levelOrder is the common reasoning scale, ascending.
var levelOrder = []ReasoningLevel{ReasoningLow, ReasoningMedium, ReasoningHigh, ReasoningXHigh, ReasoningMax}

func levelRank(l ReasoningLevel) int {
	for i, o := range levelOrder {
		if o == l {
			return i
		}
	}
	return -1
}

func parseModels(raw []byte) (*modelData, error) {
	var f modelsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if len(f.Providers) == 0 {
		return nil, fmt.Errorf("no providers")
	}
	data := &modelData{
		providers: map[string]ProviderModels{},
		windows:   map[string]int{},
		prices:    map[string]Price{},
	}
	owner := map[string]string{}
	for name, p := range f.Providers {
		pm := ProviderModels{Levels: p.Levels, Budget: p.Budget}
		prev := -1
		for _, l := range p.Levels {
			rank := levelRank(l)
			if rank < 0 {
				return nil, fmt.Errorf("%s: unknown level %q", name, l)
			}
			if rank <= prev {
				return nil, fmt.Errorf("%s: levels must be ascending", name)
			}
			prev = rank
		}
		ids := map[string]modelFile{}
		for _, m := range p.Models {
			if m.ID == "" {
				return nil, fmt.Errorf("%s: model without id", name)
			}
			if m.Window <= 0 {
				return nil, fmt.Errorf("%s: model %q needs a window above zero", name, m.ID)
			}
			if first, dup := owner[m.ID]; dup {
				return nil, fmt.Errorf("model %q is in both %s and %s", m.ID, first, name)
			}
			if m.Price != nil {
				if err := m.Price.validate(); err != nil {
					return nil, fmt.Errorf("%s: model %q: %w", name, m.ID, err)
				}
				data.prices[m.ID] = *m.Price
			}
			owner[m.ID] = name
			ids[m.ID] = m
			data.windows[m.ID] = m.Window
		}
		for _, m := range p.Models {
			if m.Alias == "" {
				pm.Models = append(pm.Models, m.ID)
				continue
			}
			target, ok := ids[m.Alias]
			if !ok || target.Alias != "" {
				return nil, fmt.Errorf("%s: alias %q points at %q, which is not a model of the provider",
					name, m.ID, m.Alias)
			}
			if _, own := data.prices[m.ID]; !own {
				if price, ok := data.prices[m.Alias]; ok {
					data.prices[m.ID] = price
				}
			}
		}
		data.providers[name] = pm
	}
	return data, nil
}

func (p Price) validate() error {
	if !p.Rates.valid() || (p.OffPeak != nil && !p.OffPeak.valid()) {
		return fmt.Errorf("a price cannot be negative")
	}
	if p.Over != nil && (!p.Over.Rates.valid() || p.Over.Above <= 0) {
		return fmt.Errorf("over needs above > 0 and no negative rate")
	}
	return nil
}

// LookupPrice returns the price of a model, matched like ContextWindow: exact
// first, then the longest prefix. ok is false for a model without a price.
func LookupPrice(model string) (Price, bool) {
	data := currentModels.Load()
	key, ok := matchModel(data.windows, model)
	if !ok {
		return Price{}, false
	}
	p, ok := data.prices[key]
	return p, ok
}

// LoadModels replaces the model data with the contents of a models file. The
// file replaces the embedded data as a whole, nothing is merged.
func LoadModels(raw []byte) error {
	data, err := parseModels(raw)
	if err != nil {
		return err
	}
	currentModels.Store(data)
	return nil
}

// ModelsPath returns where the user override lives, or "" when there is no
// user config dir.
func ModelsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "llm-bridge", modelsFileName)
}

// LoadUserModels loads the user override if the file exists. It returns the
// path it loaded, or "" when there is no file. On an invalid file the embedded
// data stays in place and the error says what is wrong.
func LoadUserModels() (string, error) {
	path := ModelsPath()
	if path == "" {
		return "", nil
	}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return path, err
	}
	if err := LoadModels(raw); err != nil {
		return path, fmt.Errorf("%s: %w", path, err)
	}
	return path, nil
}

// Catalog returns the models and reasoning controls of a provider. ok is false
// for a provider the bridge does not know, which a client still gets announced
// with its name and current model alone.
func Catalog(name string) (ProviderModels, bool) {
	pm, ok := currentModels.Load().providers[name]
	return pm, ok
}
