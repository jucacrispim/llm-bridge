package context

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEntryRender(t *testing.T) {
	e := Entry{Path: "/home/u/.llm-bridge/rules.md", Content: "line 1\nline 2"}
	want := "--- CONTEXT ENTRY BEGIN ---\n" +
		"[/home/u/.llm-bridge/rules.md]\n" +
		"line 1\nline 2\n" +
		"--- CONTEXT ENTRY END ---"
	if got := e.Render(); got != want {
		t.Errorf("Render() =\n%q\nwant\n%q", got, want)
	}
}

func TestReadDirOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "b.md"), "beta")
	writeFile(t, filepath.Join(dir, "a.md"), "alpha")
	writeFile(t, filepath.Join(dir, "c.md"), "charlie")
	writeFile(t, filepath.Join(dir, "not-md.txt"), "ignored")

	entries, err := readDir(dir)
	if err != nil {
		t.Fatalf("readDir error: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}
	// alphabetical order: a.md, b.md, c.md
	if entries[0].Content != "alpha" || entries[1].Content != "beta" || entries[2].Content != "charlie" {
		t.Fatalf("entries out of order: %+v", entries)
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Path, ".md") {
			t.Errorf("path %q should end with .md", e.Path)
		}
	}
}

func TestReadDirMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	entries, err := readDir(dir)
	if err != nil {
		t.Fatalf("readDir on missing dir should not error, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len = %d, want 0", len(entries))
	}
}

func TestReadDirNotADirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file.md")
	writeFile(t, file, "x")
	entries, err := readDir(file)
	if err != nil {
		t.Fatalf("readDir on file should not error, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len = %d, want 0", len(entries))
	}
}

func TestLoadOrder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()

	writeFile(t, filepath.Join(home, ".llm-bridge", "general.md"), "G")
	writeFile(t, filepath.Join(cwd, ".llm-bridge", "local.md"), "L")

	entries, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	// general before local
	if entries[0].Content != "G" || entries[1].Content != "L" {
		t.Fatalf("order wrong: general must come before local, got %+v", entries)
	}
}

func TestLoadEmptyCwdUsesGetwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// no local context: general only (empty cwd falls back to os.Getwd, without .llm-bridge)
	writeFile(t, filepath.Join(home, ".llm-bridge", "general.md"), "G")

	entries, err := Load("")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1 (general only)", len(entries))
	}
	if entries[0].Content != "G" {
		t.Fatalf("unexpected entry: %+v", entries[0])
	}
}

func TestLoadMissingDirs(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(t.TempDir(), "nao-existe")

	entries, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("len = %d, want 0", len(entries))
	}
}

func TestLoadOnlyLocal(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd := t.TempDir()
	writeFile(t, filepath.Join(cwd, ".llm-bridge", "local.md"), "L")

	entries, err := Load(cwd)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if len(entries) != 1 || entries[0].Content != "L" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestBuildMessages(t *testing.T) {
	entries := []Entry{
		{Path: "/a.md", Content: "A"},
		{Path: "/b.md", Content: "B"},
	}
	msgs := BuildMessages(entries)
	if len(msgs) != 2 {
		t.Fatalf("len = %d, want 2", len(msgs))
	}
	for i, m := range msgs {
		if m.Role != "user" {
			t.Errorf("msg %d role = %q, want user", i, m.Role)
		}
		if !strings.Contains(m.Content, entries[i].Path) {
			t.Errorf("msg %d should contain path %q", i, entries[i].Path)
		}
		if !strings.Contains(m.Content, entries[i].Content) {
			t.Errorf("msg %d should contain content %q", i, entries[i].Content)
		}
	}
}

// writeFile creates the file (and parent directories) with the given content.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}
