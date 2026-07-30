package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKnowledgeLoaderLoadsParentBeforeChildAndIncludes(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "root\n@include docs/shared.md\n")
	mustWrite(t, filepath.Join(root, "docs", "shared.md"), "shared\n")
	mustWrite(t, filepath.Join(root, "pkg", "RULES.md"), "child\n")

	documents, err := (KnowledgeLoader{Workspace: root}).Load([]string{"pkg/file.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 2 {
		t.Fatalf("expected 2 documents, got %+v", documents)
	}
	if !strings.Contains(documents[0].Content, "root\nshared") || documents[1].Content != "child\n" {
		t.Fatalf("unexpected document order/content: %+v", documents)
	}
}

func TestKnowledgeLoaderRejectsIncludeCycle(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "@include rules/a.md\n")
	mustWrite(t, filepath.Join(root, "rules", "a.md"), "@include ../AGENTS.md\n")

	_, err := (KnowledgeLoader{Workspace: root}).Load(nil)
	if err == nil || !strings.Contains(err.Error(), "include cycle") {
		t.Fatalf("expected cycle error, got %v", err)
	}
}

func TestKnowledgeLoaderLoadsSymlinkedRootFileWithinWorkspace(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "CLAUDE.md"), "project rules\n")
	if err := os.Symlink("CLAUDE.md", filepath.Join(root, "AGENTS.md")); err != nil {
		t.Fatal(err)
	}

	documents, err := (KnowledgeLoader{Workspace: root}).Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || documents[0].Content != "project rules\n" {
		t.Fatalf("unexpected documents: %+v", documents)
	}
}

func TestKnowledgeLoaderRejectsOutsideAndSymlinkIncludes(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.md")
	mustWrite(t, outside, "outside")
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "@include ../outside.md\n")
	if _, err := (KnowledgeLoader{Workspace: root}).Load(nil); err == nil {
		t.Fatal("expected outside include to fail")
	}

	mustWrite(t, filepath.Join(root, "AGENTS.md"), "@include linked.md\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := (KnowledgeLoader{Workspace: root}).Load(nil); err == nil {
		t.Fatal("expected symlink include to fail")
	}
}

func TestKnowledgeLoaderIgnoresIncludeInsideCodeFence(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "```text\n@include missing.md\n```\n")
	documents, err := (KnowledgeLoader{Workspace: root}).Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 || !strings.Contains(documents[0].Content, "@include missing.md") {
		t.Fatalf("unexpected documents: %+v", documents)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
