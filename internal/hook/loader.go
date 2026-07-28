package hook

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var defaultWorkspaceConfigPaths = []string{
	filepath.Join(".agent", "hooks.yaml"),
	filepath.Join(".agent", "hooks.yml"),
	filepath.Join(".ffcode", "hooks.yaml"),
}

// LoadWorkspace loads the first hook configuration present in workspace. A
// missing configuration is not an error and returns an empty bounded
// dispatcher, which keeps app assembly simple and nil-free.
func LoadWorkspace(workspace string) (*Dispatcher, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil, errors.New("hook workspace is required")
	}
	if explicit := strings.TrimSpace(os.Getenv("MYCODE_HOOK_CONFIG")); explicit != "" {
		if !filepath.IsAbs(explicit) {
			explicit = filepath.Join(workspace, explicit)
		}
		return LoadFile(explicit)
	}
	for _, relative := range defaultWorkspaceConfigPaths {
		path := filepath.Join(workspace, relative)
		if _, err := os.Stat(path); err == nil {
			return LoadFile(path)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat hook config %s: %w", path, err)
		}
	}
	return New(DefaultConfig()), nil
}

// Load is an alias for LoadFile.
func Load(path string) (*Dispatcher, error) { return LoadFile(path) }

// LoadFile parses a YAML hook configuration. It accepts either a hooks/events
// mapping or event names directly at the document root. A command entry may be
// a scalar shell command or a mapping with command, args, env, timeout, and
// max_output_bytes fields.
func LoadFile(path string) (*Dispatcher, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read hook config %s: %w", path, err)
	}
	config, err := ParseConfig(data)
	if err != nil {
		return nil, fmt.Errorf("decode hook config %s: %w", path, err)
	}
	dispatcher := New(config)
	return dispatcher, nil
}

// ParseConfig parses the standalone hook YAML format.
func ParseConfig(data []byte) (Config, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return Config{}, err
	}
	if len(document.Content) == 0 {
		return DefaultConfig(), nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return Config{}, errors.New("hook config root must be a mapping")
	}
	config := Config{Hooks: make(map[Event][]CommandSpec)}
	for index := 0; index+1 < len(root.Content); index += 2 {
		key := strings.ToLower(strings.TrimSpace(root.Content[index].Value))
		value := root.Content[index+1]
		switch key {
		case "timeout":
			duration, err := parseDurationNode(value)
			if err != nil {
				return Config{}, fmt.Errorf("timeout: %w", err)
			}
			config.Timeout = duration
		case "max_output_bytes", "maxoutputbytes":
			parsed, err := parsePositiveIntNode(value)
			if err != nil {
				return Config{}, fmt.Errorf("max_output_bytes: %w", err)
			}
			config.MaxOutputBytes = parsed
		case "max_depth", "maxdepth":
			parsed, err := parsePositiveIntNode(value)
			if err != nil {
				return Config{}, fmt.Errorf("max_depth: %w", err)
			}
			config.MaxDepth = parsed
		case "max_invocations", "maxinvocations":
			parsed, err := parsePositiveIntNode(value)
			if err != nil {
				return Config{}, fmt.Errorf("max_invocations: %w", err)
			}
			config.MaxInvocations = parsed
		case "failure_policy", "failure_strategy":
			config.FailurePolicy = normalizeFailurePolicy(value.Value)
		case "policies":
			policies, err := parsePolicyMap(value)
			if err != nil {
				return Config{}, err
			}
			if config.Policies == nil {
				config.Policies = make(map[Event]FailurePolicy)
			}
			for event, policy := range policies {
				config.Policies[event] = policy
			}
		case "hooks", "events", "commands":
			commands, err := parseEventMap(value)
			if err != nil {
				return Config{}, err
			}
			mergeCommandMaps(config.Hooks, commands)
		default:
			if event, err := ParseEvent(key); err == nil {
				specs, parseErr := parseCommandList(value)
				if parseErr != nil {
					return Config{}, fmt.Errorf("%s: %w", event, parseErr)
				}
				config.Hooks[event] = append(config.Hooks[event], specs...)
			} else {
				return Config{}, fmt.Errorf("unknown hook config field %q", key)
			}
		}
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config.normalized(), nil
}

func parseEventMap(node *yaml.Node) (map[Event][]CommandSpec, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("hooks must be an event mapping")
	}
	commands := make(map[Event][]CommandSpec)
	for index := 0; index+1 < len(node.Content); index += 2 {
		event, err := ParseEvent(node.Content[index].Value)
		if err != nil {
			return nil, err
		}
		specs, err := parseCommandList(node.Content[index+1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", event, err)
		}
		commands[event] = append(commands[event], specs...)
	}
	return commands, nil
}

func parseCommandList(node *yaml.Node) ([]CommandSpec, error) {
	if node == nil {
		return nil, errors.New("hook command is missing")
	}
	switch node.Kind {
	case yaml.ScalarNode, yaml.MappingNode:
		spec, err := parseCommandSpec(node)
		if err != nil {
			return nil, err
		}
		return []CommandSpec{spec}, nil
	case yaml.SequenceNode:
		result := make([]CommandSpec, 0, len(node.Content))
		for _, child := range node.Content {
			spec, err := parseCommandSpec(child)
			if err != nil {
				return nil, err
			}
			result = append(result, spec)
		}
		return result, nil
	default:
		return nil, errors.New("hook command must be a string, mapping, or list")
	}
}

func parseCommandSpec(node *yaml.Node) (CommandSpec, error) {
	if node.Kind == yaml.ScalarNode {
		command := strings.TrimSpace(node.Value)
		if command == "" {
			return CommandSpec{}, errors.New("hook command cannot be empty")
		}
		return CommandSpec{Command: command, Shell: true}, nil
	}
	if node.Kind != yaml.MappingNode {
		return CommandSpec{}, errors.New("hook command entry must be a string or mapping")
	}
	allowedFields := map[string]bool{
		"command": true, "args": true, "dir": true, "working_directory": true,
		"env": true, "shell": true, "timeout": true, "max_output_bytes": true,
	}
	for index := 0; index+1 < len(node.Content); index += 2 {
		field := strings.ToLower(strings.TrimSpace(node.Content[index].Value))
		if !allowedFields[field] {
			return CommandSpec{}, fmt.Errorf("unknown hook command field %q", node.Content[index].Value)
		}
	}
	var raw struct {
		Command          string            `yaml:"command"`
		Args             []string          `yaml:"args"`
		Dir              string            `yaml:"dir"`
		WorkingDirectory string            `yaml:"working_directory"`
		Env              map[string]string `yaml:"env"`
		Shell            bool              `yaml:"shell"`
		Timeout          yaml.Node         `yaml:"timeout"`
		MaxOutputBytes   int               `yaml:"max_output_bytes"`
	}
	if err := node.Decode(&raw); err != nil {
		return CommandSpec{}, err
	}
	spec := CommandSpec{
		Command: raw.Command, Args: raw.Args, Dir: raw.Dir,
		WorkingDirectory: raw.WorkingDirectory, Env: raw.Env, Shell: raw.Shell,
		MaxOutputBytes: raw.MaxOutputBytes,
	}
	if strings.TrimSpace(spec.Command) == "" {
		return CommandSpec{}, errors.New("hook command cannot be empty")
	}
	if raw.Timeout.Kind != 0 && strings.TrimSpace(raw.Timeout.Value) != "" {
		timeout, err := parseDurationNode(&raw.Timeout)
		if err != nil {
			return CommandSpec{}, fmt.Errorf("command timeout: %w", err)
		}
		spec.Timeout = timeout
	}
	if spec.MaxOutputBytes < 0 {
		return CommandSpec{}, errors.New("command max_output_bytes cannot be negative")
	}
	return spec, nil
}

func parsePolicyMap(node *yaml.Node) (map[Event]FailurePolicy, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, errors.New("hook policies must be an event mapping")
	}
	policies := make(map[Event]FailurePolicy)
	for index := 0; index+1 < len(node.Content); index += 2 {
		event, err := ParseEvent(node.Content[index].Value)
		if err != nil {
			return nil, err
		}
		policy := normalizeFailurePolicy(node.Content[index+1].Value)
		if !validFailurePolicy(policy) {
			return nil, fmt.Errorf("unknown hook failure policy %q for %s", node.Content[index+1].Value, event)
		}
		policies[event] = policy
	}
	return policies, nil
}

func normalizeFailurePolicy(value string) FailurePolicy {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "open", "ignore", "continue", "fail-open", "fail_open":
		return FailureOpen
	case "closed", "block", "deny", "fail-closed", "fail_closed":
		return FailureClosed
	case "abort", "error":
		return FailureAbort
	default:
		return FailurePolicy(strings.ToLower(strings.TrimSpace(value)))
	}
}

func parseDurationNode(node *yaml.Node) (time.Duration, error) {
	if node == nil {
		return 0, errors.New("duration is missing")
	}
	value := strings.TrimSpace(node.Value)
	if value == "" {
		return 0, errors.New("duration is empty")
	}
	if parsed, err := time.ParseDuration(value); err == nil {
		if parsed <= 0 {
			return 0, errors.New("duration must be positive")
		}
		return parsed, nil
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 {
		return 0, fmt.Errorf("invalid duration %q", value)
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func parsePositiveIntNode(node *yaml.Node) (int, error) {
	if node == nil {
		return 0, errors.New("value is missing")
	}
	value, err := strconv.Atoi(strings.TrimSpace(node.Value))
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("value %q must be positive", node.Value)
	}
	return value, nil
}

func mergeCommandMaps(destination, source map[Event][]CommandSpec) {
	for event, specs := range source {
		destination[event] = append(destination[event], specs...)
	}
}
