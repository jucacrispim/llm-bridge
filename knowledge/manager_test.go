package knowledge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddSearchAndShow(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	id, err := m.Add("nota A", "alguma coisa sobre A")
	if err != nil {
		t.Fatal(err)
	}
	if id != 0 {
		t.Fatalf("first id should be 0, got %d", id)
	}
	if _, err := m.Add("nota B", "outra coisa sobre B"); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 2 {
		t.Fatalf("want 2 items, got %d", m.Len())
	}
	res, err := m.Search("qualquer", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Fatalf("want 1 result, got %d", len(res))
	}
	show := m.Show()
	if show == "(knowledge base vazia)" {
		t.Fatal("Show should list labels")
	}
}

func TestEmptyShow(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	if got := m.Show(); got != "(knowledge base vazia)" {
		t.Fatalf("empty Show = %q", got)
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	base := t.TempDir()
	m, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add("nota X", "conteudo da nota X"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add("nota Y", "conteudo da nota Y"); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(base, "proj", "data.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("data.json should exist after Add: %v", err)
	}

	// Reload and confirm items + search survive.
	m2, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Len() != 2 {
		t.Fatalf("reload: want 2 items, got %d", m2.Len())
	}
	res, err := m2.Search("conteudo da nota", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 {
		t.Fatalf("reload search: want 2 results, got %d", len(res))
	}
}

func TestLoadAbsentFileStartsEmpty(t *testing.T) {
	base := t.TempDir()
	m, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if m.Len() != 0 {
		t.Fatalf("want empty manager, got %d", m.Len())
	}
}

func TestExecuteShowAndAdd(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	// Empty show.
	out, err := m.Execute(json.RawMessage(`{"command":"show"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(knowledge base vazia)" {
		t.Fatalf("empty show = %q", out)
	}
	// Add.
	out, err = m.Execute(json.RawMessage(`{"command":"add","label":"nota A","text":"alguma coisa"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "added") {
		t.Fatalf("add result = %q, want mention of added", out)
	}
	// Show now lists the label.
	out, err = m.Execute(json.RawMessage(`{"command":"show"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nota A") {
		t.Fatalf("show result = %q, want nota A", out)
	}
}

func TestExecuteSearch(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	if _, err := m.Add("nota A", "conteudo de A"); err != nil {
		t.Fatal(err)
	}
	out, err := m.Execute(json.RawMessage(`{"command":"search","query":"conteudo","limit":5}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "nota A") {
		t.Fatalf("search result = %q, want nota A", out)
	}
}

func TestExecuteErrors(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	cases := []string{
		`{"command":"bogus"}`,
		`{"command":"search"}`,           // missing query
		`{"command":"add","label":"x"}`,  // missing text
		`{"command":"add","text":"x"}`,   // missing label
		`not json`,                        // invalid args
	}
	for _, c := range cases {
		if _, err := m.Execute(json.RawMessage(c)); err == nil {
			t.Errorf("Execute(%s) should error", c)
		}
	}
}

func TestExecuteEmptyArgsDefaultError(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	// Empty/omitted command → unknown command error.
	if _, err := m.Execute(nil); err == nil {
		t.Fatal("Execute with nil args should error")
	}
}

func TestLoadCorruptJSON(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "proj", "data.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(base, "proj", fakeEmbedder{dim: 8}); err == nil {
		t.Fatal("want error on corrupt data.json")
	}
}
