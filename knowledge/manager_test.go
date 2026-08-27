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
		`{"command":"delete"}`,           // missing label
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

// TestLoadEmptyFileStartsEmpty covers the case where data.json exists but is
// empty (zero bytes): the manager starts empty instead of erroring.
func TestLoadEmptyFileStartsEmpty(t *testing.T) {
	base := t.TempDir()
	path := filepath.Join(base, "proj", "data.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if m.Len() != 0 {
		t.Fatalf("want empty manager from empty file, got %d", m.Len())
	}
}

// TestExecuteSearchNoResults covers formatResults rendering when a search
// returns no hits: the result is the plain "(no results)" string.
func TestExecuteSearchNoResults(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	out, err := m.Execute(json.RawMessage(`{"command":"search","query":"nada"}`))
	if err != nil {
		t.Fatal(err)
	}
	if out != "(no results)" {
		t.Fatalf("empty search = %q, want (no results)", out)
	}
}

// TestProjectDirFromPath covers the projectDirFromPath helper that derives the
// project key (last path element) from a base path.
func TestProjectDirFromPath(t *testing.T) {
	if got := projectDirFromPath("/a/b/c"); got != "c" {
		t.Fatalf("projectDirFromPath(/a/b/c) = %q, want c", got)
	}
	if got := projectDirFromPath("proj"); got != "proj" {
		t.Fatalf("projectDirFromPath(proj) = %q, want proj", got)
	}
	if got := projectDirFromPath("/"); got != "/" {
		t.Fatalf("projectDirFromPath(/) = %q, want /", got)
	}
}

// TestAddUpsertByLabel verifies that re-adding an existing label replaces its
// text/vector instead of inserting a duplicate, keeping the same id and item
// count.
func TestAddUpsertByLabel(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	id1, err := m.Add("nota", "versao 1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := m.Add("nota", "versao 2")
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("upsert should reuse the same id, got %d then %d", id1, id2)
	}
	if m.Len() != 1 {
		t.Fatalf("want 1 item after upsert, got %d", m.Len())
	}
	if m.items[0].Payload.Text != "versao 2" {
		t.Fatalf("want updated text, got %q", m.items[0].Payload.Text)
	}
	// the updated item is searchable under the same label
	res, err := m.Search("versao 2", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Label != "nota" {
		t.Fatalf("want 1 updated result, got %+v", res)
	}
}

// TestDelete verifies Delete removes an item by label (rebuilding the index so
// it no longer appears in search) and reports not-found on a missing label.
func TestDelete(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	if _, err := m.Add("a", "texto a"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add("b", "texto b"); err != nil {
		t.Fatal(err)
	}
	ok, err := m.Delete("a")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want Delete to find and remove 'a'")
	}
	if m.Len() != 1 {
		t.Fatalf("want 1 item after delete, got %d", m.Len())
	}
	res, err := m.Search("texto a", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.Label == "a" {
			t.Fatalf("deleted label 'a' still in results: %+v", res)
		}
	}
	// deleting again → not found
	ok, _ = m.Delete("a")
	if ok {
		t.Fatal("want Delete to report not found on second call")
	}
}

// TestResetRebuildsFromSeed verifies that Reset clears the KB (including manual
// adds) and rebuilds it from the seed .md files.
func TestResetRebuildsFromSeed(t *testing.T) {
	root := t.TempDir()
	writeSeed(t, root, "global.md", "conteudo global")

	m := NewManager(fakeEmbedder{dim: 8}, "llm-bridge")
	m.SetSeedRoot(root)
	if err := m.EnsureSeeded(root); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add("manual", "nota da conversa"); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 2 {
		t.Fatalf("want 2 items before reset, got %d", m.Len())
	}

	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 1 {
		t.Fatalf("want 1 item after reset (seed only), got %d", m.Len())
	}
	res, err := m.Search("conteudo global", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Label != "seed:global.md" {
		t.Fatalf("want seeded item after reset, got %+v", res)
	}
}

// TestResetPersistenceRemovesFile verifies that Reset on a persisted manager
// (dir set, e.g. from Load) also removes the data.json on disk, so the KB is
// truly wiped and not reloaded with stale items.
func TestResetPersistenceRemovesFile(t *testing.T) {
	base := t.TempDir()
	m, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Add("x", "conteudo"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "proj", "data.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("data.json should exist after Add: %v", err)
	}
	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("data.json should be removed by Reset, got err=%v", err)
	}
	// a fresh Load of the same project starts empty
	m2, err := Load(base, "proj", fakeEmbedder{dim: 8})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Len() != 0 {
		t.Fatalf("want empty KB after persisted Reset, got %d", m2.Len())
	}
}

// TestResetWithoutSeedRoot verifies that Reset with no seed root configured
// simply wipes the KB (hard reset) without error.
func TestResetWithoutSeedRoot(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	if _, err := m.Add("x", "conteudo"); err != nil {
		t.Fatal(err)
	}
	if err := m.Reset(); err != nil {
		t.Fatal(err)
	}
	if m.Len() != 0 {
		t.Fatalf("want empty KB after hard reset, got %d", m.Len())
	}
}

// TestExecuteDeleteAndReset covers the 'delete' and 'reset' commands through the
// Execute entry point (the path the tool uses), including delete of a missing
// label returning a plain notice.
func TestExecuteDeleteAndReset(t *testing.T) {
	m := NewManager(fakeEmbedder{dim: 8}, "proj")
	if _, err := m.Add("nota", "conteudo"); err != nil {
		t.Fatal(err)
	}
	out, err := m.Execute(json.RawMessage(`{"command":"delete","label":"nota"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "deleted") {
		t.Fatalf("delete result = %q, want mention of deleted", out)
	}
	if m.Len() != 0 {
		t.Fatalf("want 0 after delete, got %d", m.Len())
	}
	// delete missing label → plain notice, no error
	out, err = m.Execute(json.RawMessage(`{"command":"delete","label":"nao-existe"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "not found") {
		t.Fatalf("delete-missing result = %q, want not found", out)
	}
	// reset on empty manager → clears fine
	out, err = m.Execute(json.RawMessage(`{"command":"reset"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "reset") {
		t.Fatalf("reset result = %q, want mention of reset", out)
	}
}
