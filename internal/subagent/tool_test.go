package subagent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"FFCode/internal/agent"
)

type recordingDelegator struct {
	request Request
	result  Result
}

func (d *recordingDelegator) Delegate(_ context.Context, request Request) Result {
	d.request = request
	return d.result
}

func TestDelegateTaskSchemaIsReadOnlyAndRequiresTask(t *testing.T) {
	tool := NewDelegateTool(&recordingDelegator{})
	schema := tool.Schema()
	if schema.Name != "delegate_task" || schema.Access != "read" {
		t.Fatalf("schema = %+v", schema)
	}
	required, _ := schema.Parameters["required"].([]string)
	if len(required) != 1 || required[0] != "task" {
		t.Fatalf("required = %#v", schema.Parameters["required"])
	}
}

func TestDelegateTaskParsesBudgetAndReturnsStableJSON(t *testing.T) {
	delegator := &recordingDelegator{result: Result{Status: StatusCompleted, Summary: "done"}}
	tool := NewDelegateTool(delegator)
	result := tool.Execute(context.Background(), map[string]any{
		"task":              "inspect",
		"context":           "focus on permissions",
		"max_duration_ms":   float64(1500),
		"max_input_tokens":  float64(1200),
		"max_output_tokens": float64(300),
		"max_tool_calls":    float64(7),
	})
	if result.IsError {
		t.Fatalf("tool result = %+v", result)
	}
	if delegator.request.Task != "inspect" || delegator.request.AdditionalContext != "focus on permissions" {
		t.Fatalf("request = %+v", delegator.request)
	}
	wantBudget := agent.RunBudget{MaxDuration: 1500 * time.Millisecond, MaxInputTokens: 1200, MaxOutputTokens: 300, MaxToolCalls: 7}
	if delegator.request.Budget != wantBudget {
		t.Fatalf("budget = %+v, want %+v", delegator.request.Budget, wantBudget)
	}
	var decoded Result
	if err := json.Unmarshal([]byte(result.Output), &decoded); err != nil {
		t.Fatalf("decode result: %v\n%s", err, result.Output)
	}
	if decoded.Status != StatusCompleted || decoded.Summary != "done" {
		t.Fatalf("decoded = %+v", decoded)
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(result.Output), &wire); err != nil {
		t.Fatal(err)
	}
	usage, _ := wire["usage"].(map[string]any)
	if _, ok := usage["input_tokens"]; !ok {
		t.Fatalf("usage wire fields = %#v", usage)
	}
	if _, ok := usage["InputTokens"]; ok {
		t.Fatalf("usage leaked Go field names: %#v", usage)
	}
}

func TestDelegateTaskRejectsInvalidInput(t *testing.T) {
	tool := NewDelegateTool(&recordingDelegator{})
	for name, arguments := range map[string]map[string]any{
		"missing task":     {},
		"negative tokens":  {"task": "inspect", "max_input_tokens": float64(-1)},
		"fractional calls": {"task": "inspect", "max_tool_calls": 1.5},
	} {
		t.Run(name, func(t *testing.T) {
			result := tool.Execute(context.Background(), arguments)
			if !result.IsError || result.Output == "" {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestDelegateTaskReturnsChildFailureAsConsumableJSON(t *testing.T) {
	delegator := &recordingDelegator{result: Result{Status: StatusFailed, Error: "provider failed"}}
	result := NewDelegateTool(delegator).Execute(context.Background(), map[string]any{"task": "inspect"})
	if result.IsError {
		t.Fatalf("child failure should remain a normal tool result: %+v", result)
	}
	var decoded Result
	if err := json.Unmarshal([]byte(result.Output), &decoded); err != nil || decoded.Status != StatusFailed {
		t.Fatalf("decoded = %+v, err = %v", decoded, err)
	}
}
