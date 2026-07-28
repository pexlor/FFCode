package agent

import (
	"context"
	"testing"

	"MyCode/internal/llm"
	"MyCode/internal/permission"
	"MyCode/internal/tool"
)

type blockingPermissionManager struct {
	started chan struct{}
	release chan struct{}
}

func (m blockingPermissionManager) Authorize(context.Context, permission.PermissionRequest) (permission.PermissionResult, error) {
	close(m.started)
	<-m.release
	return permission.PermissionResult{Decision: permission.Deny, Reason: "test released"}, nil
}

type stubTool struct{}

func (stubTool) Name() string        { return "stub" }
func (stubTool) Description() string { return "test tool" }
func (stubTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: "stub", Parameters: map[string]any{"type": "object"}}
}
func (stubTool) Execute(context.Context, map[string]any) tool.ToolResult {
	return tool.ToolResult{Output: "unexpected execution"}
}

func TestExecuteToolsPreservesToolUseIDWhenTurnIsCancelled(t *testing.T) {
	manager := tool.NewToolsManager()
	permissions := blockingPermissionManager{started: make(chan struct{}), release: make(chan struct{})}
	manager.SetPermissionManager(permissions)
	manager.RegisterTool(stubTool{})
	agent := &Agent{toolManager: manager}

	ctx, cancel := context.WithCancel(context.Background())
	results := make(chan []toolResult, 1)
	events := make(chan AgentEvent, 8)
	go func() {
		got := agent.executeTools(ctx, []llm.ToolCallComplete{{ToolID: "call-1", ToolName: "stub"}}, events)
		converted := make([]toolResult, len(got))
		for index, result := range got {
			converted[index] = toolResult{id: result.ToolUseID, content: result.Content, isError: result.IsError}
		}
		results <- converted
	}()

	<-permissions.started
	cancel()
	got := <-results
	close(permissions.release)

	if len(got) != 1 {
		t.Fatalf("result count = %d, want 1", len(got))
	}
	if got[0].id != "call-1" {
		t.Fatalf("tool use id = %q, want call-1", got[0].id)
	}
	if !got[0].isError || got[0].content == "" {
		t.Fatalf("cancelled result = %+v, want non-empty error", got[0])
	}
}

type toolResult struct {
	id      string
	content string
	isError bool
}
