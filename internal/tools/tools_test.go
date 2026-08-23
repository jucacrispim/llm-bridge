package tools

import "testing"

func TestAllReturnsSixTools(t *testing.T) {
	got := All()
	if len(got) != 6 {
		t.Fatalf("len(All()) = %d, want 6", len(got))
	}
	names := map[string]bool{}
	for _, tool := range got {
		names[tool.Name] = true
	}
	expected := []string{"read", "write", "shell", "grep", "glob", "code"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("missing tool %q", name)
		}
	}
}
