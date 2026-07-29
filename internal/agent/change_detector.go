package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultDiffLimit  = 2 << 20
	statusOutputLimit = 4 << 20
	gitErrorLimit     = 8 << 10
)

type workspaceFileState struct {
	Status                 string
	WorktreeHash           string
	IndexHash              string
	TestExpectationChanged bool
}

type WorkspaceSnapshot struct {
	Root      string
	Files     map[string]workspaceFileState
	PatchHash string
	Complete  bool
}

type ChangeReport struct {
	Changes  []WorkspaceChange
	Complete bool
}

type ChangeDetector interface {
	Snapshot(context.Context, string) (WorkspaceSnapshot, error)
	Compare(WorkspaceSnapshot, WorkspaceSnapshot) (ChangeReport, error)
}

type gitChangeDetector struct {
	maxDiffBytes int
}

func newGitChangeDetector(maxDiffBytes int) *gitChangeDetector {
	if maxDiffBytes <= 0 {
		maxDiffBytes = defaultDiffLimit
	}
	return &gitChangeDetector{maxDiffBytes: maxDiffBytes}
}

func (d *gitChangeDetector) Snapshot(ctx context.Context, workspace string) (WorkspaceSnapshot, error) {
	rootOutput, _, err := runGitBounded(ctx, workspace, 4<<10, "rev-parse", "--show-toplevel")
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("resolve git workspace: %w", err)
	}
	root := strings.TrimSpace(string(rootOutput))
	if root == "" {
		return WorkspaceSnapshot{}, fmt.Errorf("resolve git workspace: empty repository root")
	}

	statusOutput, statusComplete, err := runGitBounded(ctx, root, statusOutputLimit, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("inspect git status: %w", err)
	}
	states, err := parsePorcelainStatus(statusOutput)
	if err != nil {
		return WorkspaceSnapshot{}, err
	}
	for path, state := range states {
		state.WorktreeHash = hashWorkspaceFile(filepath.Join(root, filepath.FromSlash(path)))
		state.IndexHash = hashGitIndexFile(ctx, root, path)
		states[path] = state
	}

	patch, patchComplete, err := runGitBounded(ctx, root, d.maxDiffBytes, "diff", "--no-ext-diff", "--binary", "HEAD", "--")
	if err != nil {
		return WorkspaceSnapshot{}, fmt.Errorf("inspect git diff: %w", err)
	}
	if patchComplete {
		for path := range riskyTestPathsFromDiff(patch) {
			state, exists := states[path]
			if !exists {
				continue
			}
			state.TestExpectationChanged = true
			states[path] = state
		}
	}
	digest := sha256.Sum256(patch)
	return WorkspaceSnapshot{
		Root: root, Files: states, PatchHash: hex.EncodeToString(digest[:]),
		Complete: statusComplete && patchComplete,
	}, nil
}

func (d *gitChangeDetector) Compare(baseline, current WorkspaceSnapshot) (ChangeReport, error) {
	if baseline.Root == "" || current.Root == "" || baseline.Root != current.Root {
		return ChangeReport{}, fmt.Errorf("compare workspace snapshots from different repositories")
	}
	paths := make(map[string]struct{}, len(baseline.Files)+len(current.Files))
	for path := range baseline.Files {
		paths[path] = struct{}{}
	}
	for path := range current.Files {
		paths[path] = struct{}{}
	}

	changes := make([]WorkspaceChange, 0, len(paths))
	for path := range paths {
		before, beforeExists := baseline.Files[path]
		after, afterExists := current.Files[path]
		if beforeExists == afterExists && before == after {
			continue
		}
		operation := ChangeModified
		switch {
		case afterExists && statusHas(after.Status, 'D'):
			operation = ChangeDeleted
		case !beforeExists && afterExists && (after.Status == "??" || statusHas(after.Status, 'A')):
			operation = ChangeAdded
		}
		kind := classifyChangePath(path)
		changes = append(changes, WorkspaceChange{
			Path: path, Kind: kind, Operation: operation,
			TestExpectationChanged: kind == ChangeTest && (operation == ChangeDeleted || after.TestExpectationChanged),
		})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return ChangeReport{Changes: changes, Complete: baseline.Complete && current.Complete}, nil
}

func parsePorcelainStatus(output []byte) (map[string]workspaceFileState, error) {
	states := make(map[string]workspaceFileState)
	for offset := 0; offset < len(output); {
		end := indexByte(output, 0, offset)
		if end < 0 {
			return nil, fmt.Errorf("parse git status: unterminated entry")
		}
		entry := string(output[offset:end])
		offset = end + 1
		if len(entry) < 4 || entry[2] != ' ' {
			return nil, fmt.Errorf("parse git status: invalid entry %q", entry)
		}
		status := entry[:2]
		path := filepath.ToSlash(entry[3:])
		states[path] = workspaceFileState{Status: status}
		if statusHas(status, 'R') || statusHas(status, 'C') {
			oldEnd := indexByte(output, 0, offset)
			if oldEnd < 0 {
				return nil, fmt.Errorf("parse git status: unterminated rename entry")
			}
			oldPath := filepath.ToSlash(string(output[offset:oldEnd]))
			states[oldPath] = workspaceFileState{Status: " D"}
			offset = oldEnd + 1
		}
	}
	return states, nil
}

func indexByte(data []byte, target byte, start int) int {
	for index := start; index < len(data); index++ {
		if data[index] == target {
			return index
		}
	}
	return -1
}

func statusHas(status string, value byte) bool {
	return len(status) == 2 && (status[0] == value || status[1] == value)
}

func hashWorkspaceFile(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()
	return streamHash(file)
}

func hashGitIndexFile(ctx context.Context, root, path string) string {
	command := exec.CommandContext(ctx, "git", "-C", root, "show", ":"+path)
	digest := sha256.New()
	command.Stdout = digest
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return ""
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func streamHash(reader io.Reader) string {
	digest := sha256.New()
	if _, err := io.Copy(digest, reader); err != nil {
		return ""
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func classifyChangePath(path string) ChangeKind {
	lower := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(lower)
	segments := strings.Split(lower, "/")
	for _, segment := range segments[:max(0, len(segments)-1)] {
		if segment == "test" || segment == "tests" || segment == "testing" {
			return ChangeTest
		}
	}
	if strings.HasSuffix(base, "_test.go") || strings.HasPrefix(base, "test_") || strings.HasSuffix(base, "_test.py") {
		return ChangeTest
	}
	if strings.HasPrefix(lower, "docs/") || strings.HasSuffix(base, ".md") || strings.HasSuffix(base, ".rst") {
		return ChangeDocs
	}
	for _, extension := range []string{".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java", ".c", ".cc", ".cpp", ".h"} {
		if strings.HasSuffix(base, extension) {
			return ChangeSource
		}
	}
	for _, name := range []string{"go.mod", "go.sum", "package.json", "cargo.toml", "pyproject.toml"} {
		if base == name {
			return ChangeConfig
		}
	}
	for _, extension := range []string{".yaml", ".yml", ".json", ".toml"} {
		if strings.HasSuffix(base, extension) {
			return ChangeConfig
		}
	}
	return ChangeUnknown
}

func riskyTestPathsFromDiff(diff []byte) map[string]struct{} {
	risky := make(map[string]struct{})
	currentPath := ""
	for _, line := range strings.Split(string(diff), "\n") {
		switch {
		case strings.HasPrefix(line, "+++ b/"):
			currentPath = filepath.ToSlash(strings.TrimPrefix(line, "+++ b/"))
		case strings.HasPrefix(line, "+++ /dev/null"):
			currentPath = ""
		case currentPath != "" && strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---"):
			lower := strings.ToLower(line)
			for _, marker := range []string{"assert", "expect", "t.fail", "t.error", "fatalf", "pytest.raises", "self.assert"} {
				if strings.Contains(lower, marker) {
					risky[currentPath] = struct{}{}
					break
				}
			}
		}
	}
	return risky
}

type limitedBuffer struct {
	data     []byte
	limit    int
	exceeded bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := b.limit - len(b.data)
	if remaining < len(data) {
		b.exceeded = true
		if remaining < 0 {
			remaining = 0
		}
		data = data[:remaining]
	}
	b.data = append(b.data, data...)
	return originalLength, nil
}

func runGitBounded(ctx context.Context, root string, limit int, arguments ...string) ([]byte, bool, error) {
	stdout := &limitedBuffer{limit: limit}
	stderr := &limitedBuffer{limit: gitErrorLimit}
	commandArguments := append([]string{"-C", root}, arguments...)
	command := exec.CommandContext(ctx, "git", commandArguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, false, fmt.Errorf("git %s: %w: %s", arguments[0], err, strings.TrimSpace(string(stderr.data)))
	}
	return stdout.data, !stdout.exceeded, nil
}
