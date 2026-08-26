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
// The label for each seed file is derived from its path; the body text is the
// raw file content. Empty or unreadable files are skipped.
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
		label := "seed:" + filepath.Base(f)
		if _, err := m.Add(label, string(content)); err != nil {
			return err
		}
	}
	return nil
}
