package skill

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxSkillBytes int64 = 256 * 1024

type frontMatter struct {
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	Mode         Mode     `yaml:"mode"`
	AllowedTools []string `yaml:"allowedTools"`
	ArgumentHint string   `yaml:"argumentHint"`
}

func scanSource(source Source) ([]Definition, error) {
	if source.Root == "" {
		return nil, nil
	}
	info, err := os.Stat(source.Root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read skill source %s: %w", source.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("skill source %s is not a directory", source.Root)
	}
	definitions := []Definition{}
	err = filepath.WalkDir(source.Root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if path != source.Root && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(entry.Name()) != ".md" {
			return nil
		}
		// A directory skill has exactly one entry point. Other Markdown files in
		// that directory are references and are never discovered as skills.
		if entry.Name() == "SKILL.md" || filepath.Dir(path) == source.Root || !hasSkillAncestor(path, source.Root) {
			definition, parseErr := parseFile(source, path)
			if parseErr != nil {
				return parseErr
			}
			definitions = append(definitions, definition)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan skills in %s: %w", source.Root, err)
	}
	return definitions, nil
}

func hasSkillAncestor(path, root string) bool {
	for directory := filepath.Dir(path); directory != root && directory != "."; directory = filepath.Dir(directory) {
		if info, err := os.Stat(filepath.Join(directory, "SKILL.md")); err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func parseFile(source Source, path string) (Definition, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Definition{}, err
	}
	if info.Size() > maxSkillBytes {
		return Definition{}, fmt.Errorf("skill %s exceeds %d bytes", path, maxSkillBytes)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Definition{}, err
	}
	metadata, body, err := parseFrontMatter(string(content))
	if err != nil {
		return Definition{}, fmt.Errorf("parse skill %s: %w", path, err)
	}
	definition := Definition{Name: normalizeName(metadata.Name), Description: strings.TrimSpace(metadata.Description), ArgumentHint: strings.TrimSpace(metadata.ArgumentHint), Mode: metadata.Mode, AllowedTools: normalizeTools(metadata.AllowedTools), Body: strings.TrimSpace(body), Source: SourceRef{Scope: source.Scope, Path: path}, SHA256: checksum(string(content))}
	if definition.Mode == "" {
		definition.Mode = Inline
	}
	if err := validateDefinition(definition); err != nil {
		return Definition{}, fmt.Errorf("%s: %w", path, err)
	}
	return definition, nil
}

func parseFrontMatter(content string) (frontMatter, string, error) {
	content = strings.TrimPrefix(content, "\ufeff")
	if !strings.HasPrefix(content, "---\n") && !strings.HasPrefix(content, "---\r\n") {
		return frontMatter{}, "", fmt.Errorf("missing YAML front matter")
	}
	newline := "\n"
	if strings.HasPrefix(content, "---\r\n") {
		newline = "\r\n"
	}
	rest := content[len("---")+len(newline):]
	marker := newline + "---" + newline
	index := strings.Index(rest, marker)
	if index < 0 {
		return frontMatter{}, "", fmt.Errorf("front matter is not closed")
	}
	var metadata frontMatter
	if err := yaml.Unmarshal([]byte(rest[:index]), &metadata); err != nil {
		return frontMatter{}, "", err
	}
	return metadata, rest[index+len(marker):], nil
}

func normalizeTools(tools []string) []string {
	seen := make(map[string]struct{}, len(tools))
	result := make([]string, 0, len(tools))
	for _, name := range tools {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; !exists {
			seen[name] = struct{}{}
			result = append(result, name)
		}
	}
	return result
}
