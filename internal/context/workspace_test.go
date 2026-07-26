package contextmanager

import (
	"os"
	"path/filepath"
	"testing"
)

func TestActivePathsIgnoresWorkingDirectoryVariants(t *testing.T) {
	messages := []StoredMessage{
		{ToolUses: []StoredToolUse{{
			ToolUseID: "call-1",
			Arguments: map[string]any{
				"file_path":         "src/main.go",
				"working_directory": "/repository/root",
				"working-directory": "/repository/root",
				"workingDirectory":  "/repository/root",
			},
		}}},
		{ToolResults: []StoredToolResult{{ToolUseID: "call-1"}}},
	}

	got := activePaths(messages)
	if len(got) != 1 || got[0] != "src/main.go" {
		t.Fatalf("activePaths() = %#v, want only the actual file path", got)
	}
}

func TestDemandLoaderLoadsProjectKnowledge(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("build with go test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := (DemandLoader{Workspace: root}).LoadRules(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Content != "build with go test\n" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}
