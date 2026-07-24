package contextmanager

import "testing"

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
