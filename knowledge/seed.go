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
	"os"
	"path/filepath"
)

// EnsureSeeded populates the knowledge base from seed .md files the first time
// it is used for a project, so the KB starts with curated content instead of
// being empty. Global seeds under seedRoot/*.md are combined with project
// seeds under seedRoot/<project>/*.md. It is idempotent: if the index already
// has items, nothing is embedded.
//
// The label for each seed file is the path relative to seedRoot (prefixed
// with "seed:"), so global and per-project files with the same basename never
// collide under the Manager's label-keyed upsert; the body text is the raw
// file content. Empty or unreadable files are skipped.
func (m *Manager) EnsureSeeded(seedRoot string) error {
	if m.idx.Len() > 0 {
		return nil // already seeded (or populated another way) — never repeat.
	}

	files := []string{}
	glob := func(pattern string) {
		matches, _ := filepath.Glob(pattern)
		files = append(files, matches...)
	}
	glob(filepath.Join(seedRoot, "*.md"))
	if m.project != "" {
		glob(filepath.Join(seedRoot, m.project, "*.md"))
	}

	for _, f := range files {
		content, err := os.ReadFile(f)
		if err != nil || len(content) == 0 {
			continue
		}
		label := "seed:" + seedLabelFor(seedRoot, f)
		if _, err := m.Add(label, string(content)); err != nil {
			return err
		}
	}
	return nil
}

// seedLabelFor derives the label for a seed file at `file` relative to
// `seedRoot` (slash-separated). If the two paths cannot be made relative (e.g.
// one is relative and the other absolute — `filepath.Rel` errors), it falls
// back to the file's basename so a label is always produced. Kept as a small
// helper so the fallback is independently testable.
func seedLabelFor(seedRoot, file string) string {
	rel, err := filepath.Rel(seedRoot, file)
	if err != nil {
		return filepath.ToSlash(filepath.Base(file))
	}
	return filepath.ToSlash(rel)
}
