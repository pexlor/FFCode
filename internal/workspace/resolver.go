package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// GitRootFinder returns the Git repository root containing directory.
type GitRootFinder func(directory string) (string, error)

// Current resolves the workspace from the process working directory.
// An explicit directory is used as-is; otherwise the containing Git root is
// preferred and the process working directory is the safe fallback.
func Current(explicit string) (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	return Resolve(directory, explicit, findGitRoot)
}

// Resolve determines a canonical workspace using startDirectory as the base
// for relative explicit paths and for Git repository discovery.
func Resolve(startDirectory, explicit string, findRoot GitRootFinder) (string, error) {
	start, err := canonicalDirectory(startDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve working directory: %w", err)
	}
	if strings.TrimSpace(explicit) != "" {
		candidate := explicit
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(start, candidate)
		}
		resolved, resolveErr := canonicalDirectory(candidate)
		if resolveErr != nil {
			return "", fmt.Errorf("resolve --cwd %q: %w", explicit, resolveErr)
		}
		return resolved, nil
	}
	if findRoot == nil {
		return start, nil
	}
	root, findErr := findRoot(start)
	if findErr != nil || strings.TrimSpace(root) == "" {
		return start, nil
	}
	resolved, resolveErr := canonicalDirectory(root)
	if resolveErr != nil {
		return start, nil
	}
	return resolved, nil
}

func findGitRoot(directory string) (string, error) {
	command := exec.Command("git", "-C", directory, "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	root := strings.TrimSpace(string(output))
	if root == "" {
		return "", errors.New("git returned an empty repository root")
	}
	return root, nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", resolved)
	}
	return filepath.Clean(resolved), nil
}
