package hook

import (
	"path/filepath"
	"testing"
)

func TestDefaultWorkspaceConfigPathsUseFFCodeDirectory(t *testing.T) {
	want := filepath.Join(".ffcode", "hooks.yaml")
	for _, path := range defaultWorkspaceConfigPaths {
		if path == want {
			return
		}
	}
	t.Fatalf("default workspace hook paths %v do not include %q", defaultWorkspaceConfigPaths, want)
}
