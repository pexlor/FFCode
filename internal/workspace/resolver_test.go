package workspace

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveDefaultUsesGitRepositoryRoot(t *testing.T) {
	repository := t.TempDir()
	nested := filepath.Join(repository, "cmd", "FFCode")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	canonicalRepository, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	canonicalNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatal(err)
	}

	got, err := Resolve(nested, "", func(directory string) (string, error) {
		if directory != canonicalNested {
			t.Fatalf("git probe directory = %q, want %q", directory, canonicalNested)
		}
		return repository, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != canonicalRepository {
		t.Fatalf("workspace = %q, want repository root %q", got, canonicalRepository)
	}
}

func TestResolveExplicitDirectoryDoesNotProbeGit(t *testing.T) {
	start := t.TempDir()
	explicit := filepath.Join(start, "nested")
	if err := os.Mkdir(explicit, 0o755); err != nil {
		t.Fatal(err)
	}
	called := false
	got, err := Resolve(start, "nested", func(string) (string, error) {
		called = true
		return "", errors.New("must not be called")
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if got != want || called {
		t.Fatalf("Resolve() = %q, git called = %t; want %q without probing Git", got, called, want)
	}
}

func TestResolveFallsBackWhenGitProbeFails(t *testing.T) {
	start := t.TempDir()
	want, err := filepath.EvalSymlinks(start)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(start, "", func(string) (string, error) {
		return "", errors.New("not a Git repository")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("workspace = %q, want fallback %q", got, want)
	}
}

func TestResolveRejectsInvalidExplicitDirectory(t *testing.T) {
	if _, err := Resolve(t.TempDir(), "missing", nil); err == nil {
		t.Fatal("Resolve() accepted a missing explicit workspace")
	}
}

func TestFindGitRootFromNestedDirectory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	repository := t.TempDir()
	if output, err := exec.Command("git", "init", "--quiet", repository).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	nested := filepath.Join(repository, "internal", "feature")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := findGitRoot(nested)
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatal(err)
	}
	got, err = filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Git root = %q, want %q", got, want)
	}
}
