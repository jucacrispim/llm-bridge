package tools

import "testing"

func TestAllReturnsSevenTools(t *testing.T) {
	got := All()
	if len(got) != 7 {
		t.Fatalf("len(All()) = %d, want 7", len(got))
	}
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name] = true
	}
	expected := []string{"read", "write", "shell", "grep", "glob", "search_replace", "knowledge"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
