package tool

import "testing"

func TestToolsManagerAppliesWorkspaceDefaults(t *testing.T) {
	manager := &ToolsManager{workspace: "/repository/root"}

	bashArguments := map[string]any{"command": "pwd"}
	manager.applyWorkspaceDefaults("Bash", bashArguments)
	if got := bashArguments["working_directory"]; got != "/repository/root" {
		t.Fatalf("Bash working_directory = %#v", got)
	}

	grepArguments := map[string]any{"pattern": "TODO"}
	manager.applyWorkspaceDefaults("Grep", grepArguments)
	if got := grepArguments["path"]; got != "/repository/root" {
		t.Fatalf("Grep path = %#v", got)
	}
}
