package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type KnowledgeDocument struct {
	Path        string
	Content     string
	ContentHash string
}

type KnowledgeLoader struct {
	Workspace     string
	MaxDepth      int
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

func (l KnowledgeLoader) Load(activePaths []string) ([]KnowledgeDocument, error) {
	root, err := filepath.Abs(l.Workspace)
	if err != nil {
		return nil, err
	}
	if l.MaxDepth <= 0 {
		l.MaxDepth = 8
	}
	if l.MaxFiles <= 0 {
		l.MaxFiles = 64
	}
	if l.MaxFileBytes <= 0 {
		l.MaxFileBytes = 256 * 1024
	}
	if l.MaxTotalBytes <= 0 {
		l.MaxTotalBytes = 1024 * 1024
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		if err == nil {
			err = errors.New("workspace is not a directory")
		}
		return nil, err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}

	candidates := []string{}
	seen := make(map[string]bool)
	addDirectory := func(directory string) {
		for _, name := range []string{"AGENTS.md", "RULES.md"} {
			path := filepath.Join(directory, name)
			if !seen[path] {
				seen[path] = true
				candidates = append(candidates, path)
			}
		}
	}
	addDirectory(root)
	for _, activePath := range activePaths {
		absolute := activePath
		if !filepath.IsAbs(absolute) {
			absolute = filepath.Join(root, activePath)
		}
		absolute = filepath.Clean(absolute)
		if !within(root, absolute) {
			continue
		}
		directory := absolute
		if info, statErr := os.Stat(absolute); statErr == nil && !info.IsDir() {
			directory = filepath.Dir(absolute)
		} else if filepath.Ext(absolute) != "" {
			directory = filepath.Dir(absolute)
		}
		for {
			addDirectory(directory)
			if directory == root {
				break
			}
			parent := filepath.Dir(directory)
			if parent == directory || !within(root, parent) {
				break
			}
			directory = parent
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		di, dj := pathDepth(candidates[i]), pathDepth(candidates[j])
		if di != dj {
			return di < dj
		}
		return candidates[i] < candidates[j]
	})

	var documents []KnowledgeDocument
	loaded := 0
	var total int64
	for _, path := range candidates {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		content, err := l.loadFile(root, path, nil, &loaded, &total)
		if err != nil {
			return nil, err
		}
		if content == "" {
			continue
		}
		digest := sha256.Sum256([]byte(content))
		documents = append(documents, KnowledgeDocument{Path: path, Content: content, ContentHash: hex.EncodeToString(digest[:])})
	}
	return documents, nil
}

func (l KnowledgeLoader) loadFile(root, path string, stack []string, loaded *int, total *int64) (string, error) {
	if len(stack) >= l.MaxDepth {
		return "", fmt.Errorf("knowledge include depth exceeds %d at %s", l.MaxDepth, path)
	}
	if !within(root, path) {
		return "", fmt.Errorf("knowledge include outside workspace: %s", path)
	}
	resolvedPath, err := resolveKnowledgePath(root, path)
	if err != nil {
		return "", err
	}
	for _, item := range stack {
		if item == resolvedPath {
			return "", fmt.Errorf("knowledge include cycle: %s -> %s", strings.Join(stack, " -> "), resolvedPath)
		}
	}
	info, err := os.Stat(resolvedPath)
	if err != nil {
		return "", err
	}
	if info.Size() > l.MaxFileBytes {
		return "", fmt.Errorf("knowledge file %s exceeds %d bytes", path, l.MaxFileBytes)
	}
	if *loaded >= l.MaxFiles {
		return "", fmt.Errorf("knowledge file count exceeds %d", l.MaxFiles)
	}
	data, err := os.ReadFile(resolvedPath)
	if err != nil {
		return "", err
	}
	*loaded++
	*total += int64(len(data))
	if *total > l.MaxTotalBytes {
		return "", fmt.Errorf("knowledge content exceeds %d bytes", l.MaxTotalBytes)
	}
	lines := strings.SplitAfter(string(data), "\n")
	var builder strings.Builder
	fenced := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			fenced = !fenced
			builder.WriteString(line)
			continue
		}
		if fenced || !strings.HasPrefix(trimmed, "@include ") {
			builder.WriteString(line)
			continue
		}
		includeName := strings.TrimSpace(strings.TrimPrefix(trimmed, "@include "))
		if includeName == "" || filepath.IsAbs(includeName) {
			return "", fmt.Errorf("invalid knowledge include %q in %s", includeName, path)
		}
		includePath := filepath.Clean(filepath.Join(filepath.Dir(resolvedPath), includeName))
		included, err := l.loadFile(root, includePath, append(stack, resolvedPath), loaded, total)
		if err != nil {
			return "", err
		}
		builder.WriteString(included)
		if !strings.HasSuffix(included, "\n") {
			builder.WriteByte('\n')
		}
	}
	return builder.String(), nil
}

func resolveKnowledgePath(root, path string) (string, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	resolvedPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	if !within(resolvedRoot, resolvedPath) {
		return "", fmt.Errorf("knowledge path resolves outside workspace: %s", path)
	}
	return resolvedPath, nil
}

func within(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func pathDepth(path string) int {
	return strings.Count(filepath.Clean(path), string(filepath.Separator))
}
