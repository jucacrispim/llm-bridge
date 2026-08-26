package tools

import "testing"

func TestAllReturnsEightTools(t *testing.T) {
	got := All()
	if len(got) != 8 {
		t.Fatalf("len(All()) = %d, want 8", len(got))
	}
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name] = true
	}
	expected := []string{"read", "write", "shell", "grep", "glob", "code", "search_replace", "knowledge"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
