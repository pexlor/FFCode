package subagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"FFCode/internal/agent"
	"FFCode/internal/hook"
	"FFCode/internal/tool"
)

type Delegator interface {
	Delegate(context.Context, Request) Result
}

type DelegateTool struct {
	delegator Delegator
}

func NewDelegateTool(delegator Delegator) *DelegateTool {
	return &DelegateTool{delegator: delegator}
}

func (*DelegateTool) Name() string { return "delegate_task" }

func (*DelegateTool) Description() string {
	return "Delegate one independent read-only workspace analysis task to a subagent and return source-backed structured findings."
}

func (t *DelegateTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{
		Name: t.Name(), Description: t.Description(), Access: tool.ToolAccessRead,
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task":              map[string]any{"type": "string", "description": "A self-contained analysis task."},
				"context":           map[string]any{"type": "string", "description": "Optional constraints or relevant background."},
				"max_duration_ms":   map[string]any{"type": "integer", "minimum": 1},
				"max_input_tokens":  map[string]any{"type": "integer", "minimum": 1},
				"max_output_tokens": map[string]any{"type": "integer", "minimum": 1},
				"max_tool_calls":    map[string]any{"type": "integer", "minimum": 1},
			},
			"required": []string{"task"},
		},
	}
}

func (t *DelegateTool) Execute(ctx context.Context, arguments map[string]any) tool.ToolResult {
	if t == nil || t.delegator == nil {
		return tool.ToolResult{Output: "delegate_task is not configured", IsError: true}
	}
	request, err := parseRequest(ctx, arguments)
	if err != nil {
		return tool.ToolResult{Output: err.Error(), IsError: true}
	}
	result := t.delegator.Delegate(ctx, request)
	encoded, err := json.Marshal(result)
	if err != nil {
		return tool.ToolResult{Output: "encode subagent result: " + err.Error(), IsError: true}
	}
	return tool.ToolResult{Output: string(encoded)}
}

func parseRequest(ctx context.Context, arguments map[string]any) (Request, error) {
	task, _ := arguments["task"].(string)
	task = strings.TrimSpace(task)
	if task == "" {
		return Request{}, ErrTaskRequired
	}
	additionalContext, _ := arguments["context"].(string)
	durationMS, err := optionalPositiveInteger(arguments, "max_duration_ms")
	if err != nil {
		return Request{}, err
	}
	inputTokens, err := optionalPositiveInteger(arguments, "max_input_tokens")
	if err != nil {
		return Request{}, err
	}
	outputTokens, err := optionalPositiveInteger(arguments, "max_output_tokens")
	if err != nil {
		return Request{}, err
	}
	toolCalls, err := optionalPositiveInteger(arguments, "max_tool_calls")
	if err != nil {
		return Request{}, err
	}
	parent, _ := hook.InputFromContext(ctx)
	return Request{
		ParentSessionID:   parent.SessionID,
		Workspace:         parent.Workspace,
		Task:              task,
		AdditionalContext: strings.TrimSpace(additionalContext),
		Budget: agent.RunBudget{
			MaxDuration:     time.Duration(durationMS) * time.Millisecond,
			MaxInputTokens:  inputTokens,
			MaxOutputTokens: outputTokens,
			MaxToolCalls:    int(toolCalls),
		},
	}, nil
}

func optionalPositiveInteger(arguments map[string]any, key string) (int64, error) {
	value, exists := arguments[key]
	if !exists || value == nil {
		return 0, nil
	}
	var number float64
	switch typed := value.(type) {
	case float64:
		number = typed
	case int:
		number = float64(typed)
	case int64:
		number = float64(typed)
	default:
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	if number <= 0 || math.Trunc(number) != number || number > math.MaxInt64 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	result := int64(number)
	if key == "max_tool_calls" && result > int64(math.MaxInt) {
		return 0, errors.New("max_tool_calls is too large")
	}
	return result, nil
}
