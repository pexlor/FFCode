package subagent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestNewReadOnlyToolsExposesOnlyReadTools(t *testing.T) {
	workspace := t.TempDir()
	manager, err := newReadOnlyTools(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, schema := range manager.BuildAllSchemas() {
		names = append(names, schema.Name)
	}
	sort.Strings(names)
	want := []string{"Glob", "Grep", "ReadFile"}
	if len(names) != len(want) {
		t.Fatalf("schemas = %v, want %v", names, want)
	}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("schemas = %v, want %v", names, want)
		}
	}

	for _, denied := range []string{"WriteFile", "EditFile", "Bash", "delegate_task"} {
		result := manager.Execute(context.Background(), denied, nil)
		if !result.IsError {
			t.Fatalf("%s unexpectedly executed: %+v", denied, result)
		}
	}
}

func TestReadOnlyToolsCanReadInsideWorkspace(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager, err := newReadOnlyTools(workspace)
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Execute(context.Background(), "ReadFile", map[string]any{"file_path": path})
	if result.IsError || result.Output != "1\tevidence" {
		t.Fatalf("read result = %+v", result)
	}
}
