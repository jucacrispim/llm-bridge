package knowledge

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"llm-bridge/logger"
)

// item is a single stored document plus its embedding. The JSON schema mirrors
// the kiro format ({id, payload:{label,text}, vector}) so data.json stays a
// simple, proven shape; we are not required to be byte-compatible with kiro.
type item struct {
	ID      int    `json:"id"`
	Payload struct {
		Label string `json:"label"`
		Text  string `json:"text"`
	} `json:"payload"`
	Vector []float32 `json:"vector"`
}

// Manager ties together an Embedder, an Index and on-disk persistence. It is
// scoped to a single project; when dir is non-empty, Add persists to
// dir/<project>/data.json after each insertion.
type Manager struct {
	embed   Embedder
	idx     Index
	dir     string // base dir ("" = in-memory only, no persistence)
	project string
	items   []item
}

// NewManager returns an in-memory Manager for project backed by embed, with no
// persistence. Use Load when on-disk persistence is wanted.
func NewManager(embed Embedder, project string) *Manager {
	return &Manager{
		embed:   embed,
		idx:     NewBruteForce(),
		project: project,
	}
}

// Load returns a Manager for project whose data lives under baseDir (that is,
// baseDir/<project>/data.json). If the file exists it is read and the index is
// rebuilt from its vectors; otherwise the manager starts empty and the file is
// created on the first Add. A non-parseable data.json is returned as an error
// so callers can decide whether to warn or wipe.
func Load(baseDir, project string, embed Embedder) (*Manager, error) {
	m := NewManager(embed, project)
	m.dir = baseDir

	path := m.dataPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil // nothing persisted yet — starts empty.
		}
		return nil, err
	}
	if len(data) == 0 {
		return m, nil
	}
	var items []item
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, err
	}
	// Rebuild the index from persisted vectors.
	for _, it := range items {
		m.idx.Add(it.Vector, it.Payload.Label, it.Payload.Text)
		m.items = append(m.items, it)
	}
	return m, nil
}

// dataPath returns the full path of the project's data.json.
func (m *Manager) dataPath() string {
	return filepath.Join(m.dir, m.project, "data.json")
}

// save writes the current items to data.json. It is a no-op when the manager
// is in-memory (no base dir set).
func (m *Manager) save() error {
	if m.dir == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(m.dataPath()), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m.items, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(m.dataPath(), data, 0o644)
}

// Add embeds text with the passage prefix, stores it in the index, records the
// item and persists to disk (if persistence is enabled). It returns the
// assigned id.
func (m *Manager) Add(label, text string) (int, error) {
	vec, err := m.embed.Embed(PassagePrefix+text, false)
	if err != nil {
		return 0, err
	}
	id := m.idx.Add(vec, label, text)
	logger.Tracef("knowledge add: label=%q id=%d", label, id)

	it := item{ID: id}
	it.Payload.Label = label
	it.Payload.Text = text
	it.Vector = vec
	m.items = append(m.items, it)
	if err := m.save(); err != nil {
		return 0, err
	}
	return id, nil
}

// Search embeds query with the query prefix and returns the top-k hits.
// limit <= 0 falls back to the index default (5).
func (m *Manager) Search(query string, limit int) ([]Result, error) {
	start := time.Now()
	vec, err := m.embed.Embed(QueryPrefix+query, true)
	if err != nil {
		return nil, err
	}
	res := m.idx.Search(vec, limit)
	logger.Tracef("knowledge search: query=%q hits=%d elapsed=%s", query, len(res), time.Since(start).Round(time.Microsecond))
	return res, nil
}

// Show returns a human-readable summary of the stored items.
func (m *Manager) Show() string {
	if m.idx.Len() == 0 {
		return "(knowledge base vazia)"
	}
	var b strings.Builder
	for _, it := range m.items {
		b.WriteString(it.Payload.Label)
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Len returns the number of stored items.
func (m *Manager) Len() int { return m.idx.Len() }

// Execute handles a `knowledge` tool invocation (command: show|search|add) and
// returns the formatted result to feed back to the model as the tool result.
// args is the tool's raw JSON arguments.
func (m *Manager) Execute(args json.RawMessage) (string, error) {
	var p struct {
		Command string `json:"command"`
		Query   string `json:"query"`
		Label   string `json:"label"`
		Text    string `json:"text"`
		Limit   int    `json:"limit"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return "", fmt.Errorf("invalid knowledge arguments: %w", err)
		}
	}
	switch p.Command {
	case "show":
		return m.Show(), nil
	case "search":
		if p.Query == "" {
			return "", errors.New("knowledge search requires a query")
		}
		res, err := m.Search(p.Query, p.Limit)
		if err != nil {
			return "", err
		}
		return formatResults(res), nil
	case "add":
		if p.Label == "" || p.Text == "" {
			return "", errors.New("knowledge add requires label and text")
		}
		id, err := m.Add(p.Label, p.Text)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("added %q (id %d)", p.Label, id), nil
	default:
		return "", fmt.Errorf("unknown knowledge command %q", p.Command)
	}
}

// formatResults renders search results as text for the model.
func formatResults(res []Result) string {
	if len(res) == 0 {
		return "(no results)"
	}
	var b strings.Builder
	for _, r := range res {
		fmt.Fprintf(&b, "[%s] (score %.4f)\n%s\n\n", r.Label, r.Score, r.Text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// projectDirFromPath returns the last path element of path (used as the
// project key for seed lookup). Kept small here; the cwd wiring lives in the
// server phase.
func projectDirFromPath(path string) string {
	return filepath.Base(path)
}
