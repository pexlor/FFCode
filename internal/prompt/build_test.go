package prompt

import (
	"strings"
	"testing"
)

func TestBuildSystemPromptUsesResolvedWorkspace(t *testing.T) {
	got, err := BuildSystemPromptForWorkspace("/repository/root")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "Current working directory: /repository/root") {
		t.Fatalf("system prompt does not contain resolved workspace: %q", got)
	}
}
