package config

import (
	"fmt"
	"strings"
)

const (
	DefaultContextWindow = 128000
	DefaultOutputReserve = 8192
)

type Config struct {
	Model   ModelConfig   `yaml:"model"`
	Summary SummaryConfig `yaml:"summary"`
	Context ContextConfig `yaml:"context"`
	Memory  MemoryConfig  `yaml:"memory"`
}

type ModelConfig struct {
	Protocol  string `yaml:"protocol"`
	BaseURL   string `yaml:"base_url"`
	APIKey    string `yaml:"api_key"`
	Name      string `yaml:"name"`
	MaxTokens int    `yaml:"max_tokens"`
	// EnableThinking is passed to providers that support a non-standard
	// enable_thinking request field (for example, Qwen OpenAI-compatible APIs).
	EnableThinking bool `yaml:"enable_thinking"`
}

type SummaryConfig struct {
	Model   string `yaml:"model"`
	BaseURL string `yaml:"base_url"`
	APIKey  string `yaml:"api_key"`
}

type ContextConfig struct {
	Window        int `yaml:"window"`
	OutputReserve int `yaml:"output_reserve"`
}

type MemoryConfig struct {
	Generate              bool   `yaml:"generate"`
	Use                   bool   `yaml:"use"`
	Root                  string `yaml:"root"`
	MinSessionIdle        string `yaml:"min_session_idle"`
	ExtractionConcurrency int    `yaml:"extraction_concurrency"`
	MaxSessionsPerRun     int    `yaml:"max_sessions_per_run"`
	SummaryTokenLimit     int    `yaml:"summary_token_limit"`
	ExtractModel          string `yaml:"extract_model"`
	ConsolidationModel    string `yaml:"consolidation_model"`
	DisableOnExternal     bool   `yaml:"disable_on_external_context"`
}

func (c *Config) applyDefaults() {
	if c.Context.Window == 0 {
		c.Context.Window = DefaultContextWindow
	}
	if c.Context.OutputReserve == 0 {
		c.Context.OutputReserve = DefaultOutputReserve
	}
	if c.Summary.Model != "" && strings.TrimSpace(c.Summary.BaseURL) == "" {
		c.Summary.BaseURL = c.Model.BaseURL
	}
	if c.Memory.Root == "" {
		c.Memory.Root = ".ffcode/memory"
	}
	if c.Memory.MinSessionIdle == "" {
		c.Memory.MinSessionIdle = "30m"
	}
	if c.Memory.ExtractionConcurrency == 0 {
		c.Memory.ExtractionConcurrency = 2
	}
	if c.Memory.MaxSessionsPerRun == 0 {
		c.Memory.MaxSessionsPerRun = 100
	}
	if c.Memory.SummaryTokenLimit == 0 {
		c.Memory.SummaryTokenLimit = 8000
	}
}

func (c Config) validate() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "model.protocol", value: c.Model.Protocol},
		{name: "model.base_url", value: c.Model.BaseURL},
		{name: "model.api_key", value: c.Model.APIKey},
		{name: "model.name", value: c.Model.Name},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%s is required", field.name)
		}
	}
	if c.Model.MaxTokens < 0 {
		return fmt.Errorf("model.max_tokens must be non-negative")
	}
	if c.Context.Window <= 0 {
		return fmt.Errorf("context.window must be positive")
	}
	if c.Context.OutputReserve <= 0 {
		return fmt.Errorf("context.output_reserve must be positive")
	}
	if c.Summary.Model != "" && strings.TrimSpace(c.Summary.APIKey) == "" {
		return fmt.Errorf("summary.api_key is required when summary.model is set")
	}
	if c.Memory.ExtractionConcurrency <= 0 || c.Memory.MaxSessionsPerRun <= 0 || c.Memory.SummaryTokenLimit <= 0 {
		return fmt.Errorf("memory limits must be positive")
	}
	return nil
}
