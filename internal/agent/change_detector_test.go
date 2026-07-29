package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGitChangeDetectorIgnoresInitialDirtyStateAndFindsRunChanges(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	writeRepoFile(t, repo, "existing.go", "package sample\n// dirty before run\n")

	detector := newGitChangeDetector(2 << 20)
	baseline, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}

	writeRepoFile(t, repo, "existing.go", "package sample\n// changed during run\n")
	writeRepoFile(t, repo, "new_test.go", "package sample\n")
	current, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	report, err := detector.Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceChange(t, report.Changes, "existing.go", ChangeModified, ChangeSource)
	assertWorkspaceChange(t, report.Changes, "new_test.go", ChangeAdded, ChangeTest)
}

func TestGitChangeDetectorReportsNoChangeAfterRestore(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	detector := newGitChangeDetector(2 << 20)
	baseline, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "existing.go", "package sample\n// temporary\n")
	writeRepoFile(t, repo, "existing.go", "package sample\n")
	current, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	report, err := detector.Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Changes) != 0 {
		t.Fatalf("changes = %+v", report.Changes)
	}
}

func TestGitChangeDetectorMarksDeletedTestsAndChangedExpectations(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	writeRepoFile(t, repo, "kept_test.go", "package sample\n\nfunc TestValue(t *testing.T) {\n\tif got != 1 { t.Fatalf(\"got %d\", got) }\n}\n")
	writeRepoFile(t, repo, "deleted_test.go", "package sample\n")
	runRepoGit(t, repo, "add", ".")
	runRepoGit(t, repo, "commit", "-m", "add tests")

	detector := newGitChangeDetector(2 << 20)
	baseline, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "kept_test.go", "package sample\n\nfunc TestValue(t *testing.T) {\n\tif got != 2 { t.Fatalf(\"got %d\", got) }\n}\n")
	if err := os.Remove(filepath.Join(repo, "deleted_test.go")); err != nil {
		t.Fatal(err)
	}
	current, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	report, err := detector.Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	deleted := findWorkspaceChange(t, report.Changes, "deleted_test.go")
	if deleted.Operation != ChangeDeleted || deleted.Kind != ChangeTest || !deleted.TestExpectationChanged {
		t.Fatalf("deleted change = %+v", deleted)
	}
	changed := findWorkspaceChange(t, report.Changes, "kept_test.go")
	if !changed.TestExpectationChanged {
		t.Fatalf("changed assertion was not marked: %+v", changed)
	}
}

func TestGitChangeDetectorMarksOversizedDiffIncomplete(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	detector := newGitChangeDetector(16)
	baseline, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	writeRepoFile(t, repo, "existing.go", "package sample\n// enough content to exceed the configured diff limit\n")
	current, err := detector.Snapshot(context.Background(), repo)
	if err != nil {
		t.Fatal(err)
	}
	if current.Complete {
		t.Fatal("oversized diff was reported complete")
	}
	report, err := detector.Compare(baseline, current)
	if err != nil {
		t.Fatal(err)
	}
	if report.Complete {
		t.Fatal("comparison with incomplete snapshot was reported complete")
	}
}

func TestGitChangeDetectorRejectsNonGitWorkspace(t *testing.T) {
	_, err := newGitChangeDetector(1024).Snapshot(context.Background(), t.TempDir())
	if err == nil {
		t.Fatal("Snapshot() error = nil")
	}
}

func initChangeDetectorRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runRepoGit(t, repo, "init")
	runRepoGit(t, repo, "config", "user.email", "test@example.com")
	runRepoGit(t, repo, "config", "user.name", "FFCode Test")
	writeRepoFile(t, repo, "existing.go", "package sample\n")
	runRepoGit(t, repo, "add", ".")
	runRepoGit(t, repo, "commit", "-m", "initial")
	return repo
}

func writeRepoFile(t *testing.T, repo, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repo, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRepoGit(t *testing.T, repo string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", arguments, err, output)
	}
}

func assertWorkspaceChange(t *testing.T, changes []WorkspaceChange, path string, operation ChangeOperation, kind ChangeKind) {
	t.Helper()
	change := findWorkspaceChange(t, changes, path)
	if change.Operation != operation || change.Kind != kind {
		t.Fatalf("change %q = %+v, want operation %q kind %q", path, change, operation, kind)
	}
}

func findWorkspaceChange(t *testing.T, changes []WorkspaceChange, path string) WorkspaceChange {
	t.Helper()
	for _, change := range changes {
		if change.Path == path {
			return change
		}
	}
	t.Fatalf("change %q not found in %+v", path, changes)
	return WorkspaceChange{}
}
