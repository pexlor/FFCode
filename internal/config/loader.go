package config

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return filepath.Join(home, ".ffcode", "config.yaml"), nil
}

func Load(warnings io.Writer) (Config, error) {
	path, err := DefaultPath()
	if err != nil {
		return Config{}, err
	}
	return LoadFile(path, warnings)
}

func LoadFile(path string, warnings io.Writer) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("read config %s: %w; create it with at least:\nmodel:\n  protocol: anthropic\n  base_url: https://api.anthropic.com\n  api_key: <your-api-key>\n  name: <model-name>", path, err)
		}
		return Config{}, fmt.Errorf("read config %s: %w", path, err)
	}

	result := Config{Memory: MemoryConfig{Generate: true, Use: true}}
	if err := yaml.Unmarshal(data, &result); err != nil {
		return Config{}, fmt.Errorf("decode config %s: %w", path, err)
	}
	result.applyDefaults()
	if err := result.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %s: %w", path, err)
	}

	if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0o077 != 0 && warnings != nil {
		fmt.Fprintf(warnings, "warning: config %s is readable by other users; run chmod 600 %s\n", path, path)
	}
	return result, nil
}
