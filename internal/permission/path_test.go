package permission

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPathValidatorAllowsExistingAndNewWorkspacePaths(t *testing.T) {
	workspace := t.TempDir()
	validator, err := NewPathValidator(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(workspace, "existing.txt")
	if err := os.WriteFile(existing, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"existing.txt", filepath.Join("new", "file.txt")} {
		resolved, err := validator.Validate(path, workspace)
		if err != nil {
			t.Fatalf("Validate(%q): %v", path, err)
		}
		if !isWithin(validator.Workspace(), resolved) {
			t.Fatalf("resolved path %q is outside workspace", resolved)
		}
	}
}

func TestPathValidatorRejectsTraversalAndEscapingSymlink(t *testing.T) {
	workspace := t.TempDir()
	outside := t.TempDir()
	validator, err := NewPathValidator(workspace, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := validator.Validate(filepath.Join("..", filepath.Base(outside)), workspace); err == nil {
		t.Fatal("Validate accepted parent traversal")
	}
	link := filepath.Join(workspace, "outside-link")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := validator.Validate(link, workspace); err == nil {
		t.Fatal("Validate accepted symlink escaping the workspace")
	}
}

func TestPathValidatorRejectsProtectedPath(t *testing.T) {
	workspace := t.TempDir()
	protected := filepath.Join(workspace, "private")
	if err := os.Mkdir(protected, 0o700); err != nil {
		t.Fatal(err)
	}
	validator, err := NewPathValidator(workspace, []string{protected})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validator.Validate(filepath.Join(protected, "secret.txt"), workspace); err == nil {
		t.Fatal("Validate accepted a protected path")
	}
}
