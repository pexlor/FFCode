package memory

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectAgentFilesLoadByActivePath(t *testing.T) {
	workspace := filepath.Clean(filepath.Join("..", ".."))
	documents, err := (KnowledgeLoader{Workspace: workspace}).Load([]string{"internal/memory/service.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 {
		t.Fatalf("expected root and memory rules, got %d: %+v", len(documents), documents)
	}
	if !strings.HasSuffix(documents[0].Path, filepath.Join("FFCode", "AGENTS.md")) && !strings.HasSuffix(documents[0].Path, "AGENTS.md") {
		t.Fatalf("first document is not the project rule: %+v", documents)
	}
	if !strings.Contains(documents[0].Content, "go test ./...") {
		t.Fatalf("root agent rules were not loaded: %q", documents[0].Content)
	}
	if !strings.Contains(documents[1].Content, "RawMemory") {
		t.Fatalf("memory-specific agent rules were not loaded: %q", documents[1].Content)
	}
}

func TestProjectAgentFilesLoadStorageScope(t *testing.T) {
	workspace := filepath.Clean(filepath.Join("..", ".."))
	documents, err := (KnowledgeLoader{Workspace: workspace}).Load([]string{"internal/storage/filememory/store.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 || !strings.Contains(documents[1].Content, "Manifest") {
		t.Fatalf("storage scope did not load: %+v", documents)
	}
}
