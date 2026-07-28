package skill

import (
	"MyCode/internal/tool"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadToolDeclaresWriteAccess(t *testing.T) {
	if access := (&LoadTool{}).Schema().Access; access != tool.ToolAccessWrite {
		t.Fatalf("load_skill access = %q, want %q", access, tool.ToolAccessWrite)
	}
}

func writeSkill(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryProjectOverridesLowerScopes(t *testing.T) {
	base := t.TempDir()
	project, user, builtin := filepath.Join(base, "project"), filepath.Join(base, "user"), filepath.Join(base, "builtin")
	writeSkill(t, builtin, "review.md", "---\nname: review\ndescription: builtin\n---\nbuiltin")
	writeSkill(t, user, "review.md", "---\nname: review\ndescription: user\n---\nuser")
	writeSkill(t, project, "review.md", "---\nname: review\ndescription: project\n---\nproject")
	registry := NewRegistry([]Source{{Scope: Builtin, Root: builtin}, {Scope: User, Root: user}, {Scope: Project, Root: project}})
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	definition, err := registry.Resolve("REVIEW")
	if err != nil {
		t.Fatal(err)
	}
	if definition.Source.Scope != Project || definition.Body != "project" {
		t.Fatalf("got %#v", definition)
	}
}

func TestManagerRendersArgumentsAndIntersectsToolLists(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "one.md", "---\nname: one\ndescription: one\nallowedTools: [ReadFile, Grep]\n---\nReview $1: $ARGUMENTS")
	writeSkill(t, root, "two.md", "---\nname: two\ndescription: two\nallowedTools: [grep, Bash]\n---\nSecond")
	registry := NewRegistry([]Source{{Scope: Project, Root: root}})
	if err := registry.Reload(); err != nil {
		t.Fatal(err)
	}
	manager := NewManager(registry, func(name string) bool {
		return strings.EqualFold(name, "ReadFile") || strings.EqualFold(name, "Grep") || strings.EqualFold(name, "Bash")
	})
	active, err := manager.Load("one", "target extra")
	if err != nil {
		t.Fatal(err)
	}
	if active.Rendered != "Review target: target extra" {
		t.Fatalf("unexpected render: %q", active.Rendered)
	}
	if _, err := manager.Load("two", ""); err != nil {
		t.Fatal(err)
	}
	allowed := manager.AllowedTools()
	if len(allowed) != 1 {
		t.Fatalf("got allowed tools %#v", allowed)
	}
	if _, ok := allowed["grep"]; !ok {
		t.Fatalf("grep should remain allowed: %#v", allowed)
	}
}

func TestRegistryRejectsSameScopeDuplicate(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "a.md", "---\nname: duplicate\ndescription: first\n---\na")
	writeSkill(t, root, "nested/b.md", "---\nname: duplicate\ndescription: second\n---\nb")
	registry := NewRegistry([]Source{{Scope: Project, Root: root}})
	if err := registry.Reload(); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}
