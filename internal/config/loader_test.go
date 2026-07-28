package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileReadsThinkingEffortAndBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`model:
  protocol: openai-compat
  base_url: https://example.com
  api_key: key
  name: model
  thinking_effort: high
  thinking_budget: 12000
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model.ThinkingEffort != "high" || got.Model.ThinkingBudget != 12000 {
		t.Fatalf("thinking config = %+v", got.Model)
	}
}

func TestLoadFileRejectsInvalidThinkingEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`model:
  protocol: openai-compat
  base_url: https://example.com
  api_key: key
  name: model
  thinking_effort: extreme
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(path, nil)
	if err == nil || !strings.Contains(err.Error(), "model.thinking_effort") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadFileIgnoresEnvironmentOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte(`model:
  protocol: anthropic
  base_url: https://config.example.com
  api_key: config-key
  name: config-model
  max_tokens: 4096
  enable_thinking: false
summary:
  model: config-summary-model
  base_url: https://summary.example.com
  api_key: config-summary-key
context:
  window: 128000
  output_reserve: 8192
hooks:
  enabled: true
memory:
  generate: false
  use: true
  root: .memory-test
  min_session_idle: 45m
  extraction_concurrency: 3
  summary_token_limit: 2048
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	overrides := map[string]string{
		"MYCODE_PROTOCOL":          "openai",
		"MYCODE_BASE_URL":          "https://env.example.com",
		"ANTHROPIC_BASE_URL":       "https://anthropic-env.example.com",
		"MYCODE_API_KEY":           "env-key",
		"ANTHROPIC_API_KEY":        "anthropic-env-key",
		"MYCODE_MODEL":             "env-model",
		"MYCODE_ENABLE_THINKING":   "true",
		"MYCODE_SUMMARY_MODEL":     "env-summary-model",
		"MYCODE_SUMMARY_BASE_URL":  "https://env-summary.example.com",
		"MYCODE_SUMMARY_API_KEY":   "env-summary-key",
		"MYCODE_MAX_TOKENS":        "2048",
		"MYCODE_CONTEXT_WINDOW":    "64000",
		"MYCODE_MAX_OUTPUT_TOKENS": "4096",
	}
	for name, value := range overrides {
		t.Setenv(name, value)
	}

	got, err := LoadFile(path, nil)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	if got.Model.Protocol != "anthropic" || got.Model.BaseURL != "https://config.example.com" ||
		got.Model.APIKey != "config-key" || got.Model.Name != "config-model" ||
		got.Model.MaxTokens != 4096 || got.Model.EnableThinking {
		t.Fatalf("model config was overridden by environment: %+v", got.Model)
	}
	if got.Summary.Model != "config-summary-model" || got.Summary.BaseURL != "https://summary.example.com" ||
		got.Summary.APIKey != "config-summary-key" {
		t.Fatalf("summary config was overridden by environment: %+v", got.Summary)
	}
	if got.Context.Window != 128000 || got.Context.OutputReserve != 8192 {
		t.Fatalf("context config was overridden by environment: %+v", got.Context)
	}
	if !got.Hooks.Enabled {
		t.Fatalf("hooks config mismatch: %+v", got.Hooks)
	}
	if got.Memory.Generate || !got.Memory.Use || got.Memory.Root != ".memory-test" || got.Memory.MinSessionIdle != "45m" || got.Memory.ExtractionConcurrency != 3 || got.Memory.SummaryTokenLimit != 2048 {
		t.Fatalf("memory config mismatch: %+v", got.Memory)
	}
}

func TestMemoryConfigDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`model:
  protocol: anthropic
  base_url: https://example.com
  api_key: key
  name: model
`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Memory.Generate || !got.Memory.Use || got.Memory.Root == "" || got.Memory.ExtractionConcurrency <= 0 {
		t.Fatalf("unexpected defaults: %+v", got.Memory)
	}
}
