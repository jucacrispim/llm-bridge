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

package knowledge

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Scope names. ScopeBoth is not a KB of its own: it is a valid -kb-scope /
// kb_scope value meaning global plus project.
const (
	ScopeGlobal  = "global"
	ScopeProject = "project"
	ScopeBoth    = "both"

	// GlobalKey is the storage key, the directory name, of the shared KB.
	GlobalKey = "_global"
)

// defaultLimit is how many results a search returns when the caller does not ask.
const defaultLimit = 5

// IsScope reports whether name is a valid -kb-scope / kb_scope value.
func IsScope(name string) bool {
	return name == ScopeGlobal || name == ScopeProject || name == ScopeBoth
}

// Scope binds a scope name ("global" or "project") to the Manager that holds
// its notes.
type Scope struct {
	Name string
	M    *Manager
}

// Set is the KB surface the `knowledge` tool talks to: one or two scoped
// Managers. Reads cover every scope; writes target one, named by the tool's
// "scope" argument or the default.
type Set struct {
	scopes []Scope
	def    string
}

// NewSet returns a Set over scopes with def as the default write target. When
// def is empty (or names a scope that is not in the set) the first scope is
// used, so a Set is always usable.
func NewSet(def string, scopes ...Scope) *Set {
	s := &Set{scopes: scopes, def: def}
	if s.find(def) == nil && len(scopes) > 0 {
		s.def = scopes[0].Name
	}
	return s
}

// Scopes returns the scope names in the set, in order.
func (s *Set) Scopes() []string {
	names := make([]string, 0, len(s.scopes))
	for _, sc := range s.scopes {
		names = append(names, sc.Name)
	}
	return names
}

// Default returns the scope name used for writes when the tool does not name
// one.
func (s *Set) Default() string { return s.def }

// Len returns the total number of stored items across all scopes.
func (s *Set) Len() int {
	total := 0
	for _, sc := range s.scopes {
		total += sc.M.Len()
	}
	return total
}

// find returns the Manager for the named scope, or nil when the set has no
// such scope.
func (s *Set) find(name string) *Manager {
	for _, sc := range s.scopes {
		if sc.Name == name {
			return sc.M
		}
	}
	return nil
}

// resolve returns the manager a write should target: the scope named in the
// arguments, or the default. With one scope that KB always wins, and a
// mismatched name becomes a note for the model; with two, an unknown name is an
// error listing the available ones.
func (s *Set) resolve(name string) (*Manager, string, string, error) {
	want := name
	if name == "" {
		name = s.def
	}
	if m := s.find(name); m != nil {
		return m, name, "", nil
	}
	if len(s.scopes) == 1 {
		// The only KB is the answer whatever was asked for: no scope, its own
		// scope name, or the project key passed instead of the scope name.
		only := s.scopes[0]
		if want == "" || want == only.Name || want == only.M.Project() {
			return only.M, only.Name, "", nil
		}
		return only.M, only.Name,
			fmt.Sprintf(" (note: scope %q not active, used %q)", want, only.Name), nil
	}
	return nil, "", "", fmt.Errorf("unknown knowledge scope %q (available: %s)",
		name, strings.Join(s.Scopes(), ", "))
}

// Execute handles a `knowledge` tool call. Reads cover every scope with the
// results merged; writes go to the scope in the arguments or the default.
func (s *Set) Execute(args json.RawMessage) (string, error) {
	p, err := parseToolArgs(args)
	if err != nil {
		return "", err
	}
	switch p.Command {
	case "show":
		return s.show(), nil
	case "search":
		if p.Query == "" {
			return "", fmt.Errorf("knowledge search requires a query")
		}
		res, err := s.search(p.Query, p.Limit)
		if err != nil {
			return "", err
		}
		return formatResults(res), nil
	case "add", "delete", "reset":
		m, name, note, err := s.resolve(p.Scope)
		if err != nil {
			return "", err
		}
		out, err := m.Execute(args)
		if err != nil {
			return "", err
		}
		// Say which KB was written only when there was a choice.
		if len(s.scopes) > 1 {
			out += fmt.Sprintf(" (scope %q)", name)
		}
		return out + note, nil
	default:
		return "", fmt.Errorf("unknown knowledge command %q", p.Command)
	}
}

// show renders the labels of every scope, grouped by scope name. A single scope
// renders exactly like Manager.Show.
func (s *Set) show() string {
	if len(s.scopes) == 1 {
		return s.scopes[0].M.Show()
	}
	var b strings.Builder
	for i, sc := range s.scopes {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "[%s]\n", sc.Name)
		b.WriteString(sc.M.Show())
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// search queries every scope, merges the hits by descending score and keeps the
// top `limit` (defaultLimit when limit <= 0). With more than one scope the
// result labels are prefixed with the scope name, so the model can tell a
// global note from a project one.
func (s *Set) search(query string, limit int) ([]Result, error) {
	per := limit
	if per <= 0 {
		per = defaultLimit
	}
	multi := len(s.scopes) > 1
	all := make([]Result, 0, per*len(s.scopes))
	for _, sc := range s.scopes {
		res, err := sc.M.Search(query, per)
		if err != nil {
			return nil, err
		}
		for _, r := range res {
			if multi {
				r.Label = sc.Name + ": " + r.Label
			}
			all = append(all, r)
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Score > all[j].Score })
	if len(all) > per {
		all = all[:per]
	}
	return all, nil
}
