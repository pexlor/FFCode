package agent

import (
	"context"
	"strings"
	"sync"
	"testing"

	"MyCode/internal/llm"
	"MyCode/internal/permission"
	"MyCode/internal/tool"
)

func TestRunPhaseControllerFollowsWriteAndVerificationWork(t *testing.T) {
	controller := newRunPhaseController()
	if controller.phase() != PhaseExplore {
		t.Fatalf("initial phase = %q", controller.phase())
	}

	transition := controller.observeToolCalls([]llm.ToolCallComplete{{ToolName: "WriteFile"}})
	if transition.To != PhaseImplement {
		t.Fatalf("write transition = %+v", transition)
	}

	transition = controller.observeToolCalls([]llm.ToolCallComplete{{ToolName: "Bash", Arguments: map[string]any{"command": "go test ./..."}}})
	if transition.To != PhaseVerify {
		t.Fatalf("verification transition = %+v", transition)
	}

	transition = controller.observeToolResults(
		[]llm.ToolCallComplete{{ToolName: "Bash", Arguments: map[string]any{"command": "go test ./..."}}},
		[]tool.ToolResult{{Output: "ok"}},
	)
	if transition.To != PhaseFinalize {
		t.Fatalf("successful verification transition = %+v", transition)
	}
}

func TestRunPhaseControllerFinalizesAtSoftBudgetLimit(t *testing.T) {
	controller := newRunPhaseController()
	transition := controller.observeBudget(runBudgetSnapshot{
		Budget: RunBudget{MaxInputTokens: 100},
		Usage:  llm.UsageInfo{InputTokens: 75},
	})

	if transition.To != PhaseFinalize || transition.Reason != PhaseReasonSoftBudget {
		t.Fatalf("transition = %+v", transition)
	}
}

type phaseClient struct {
	mu        sync.Mutex
	responses [][]llm.StreamEvent
	prompts   []string
}

func (c *phaseClient) Stream(request *llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.mu.Lock()
	c.prompts = append(c.prompts, request.SystemPrompt)
	response := c.responses[0]
	c.responses = c.responses[1:]
	c.mu.Unlock()
	events := make(chan llm.StreamEvent, len(response))
	errors := make(chan error)
	for _, event := range response {
		events <- event
	}
	close(events)
	close(errors)
	return events, errors
}

type namedTool struct{ name string }

type allowPermissionManager struct{}

func (allowPermissionManager) Authorize(context.Context, permission.PermissionRequest) (permission.PermissionResult, error) {
	return permission.PermissionResult{Decision: permission.Allow}, nil
}

func (t namedTool) Name() string        { return t.name }
func (t namedTool) Description() string { return t.name }
func (t namedTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.name, Parameters: map[string]any{"type": "object"}}
}
func (t namedTool) Execute(context.Context, map[string]any) tool.ToolResult {
	return tool.ToolResult{Output: "ok"}
}

func TestAgentInjectsCurrentPhaseGuidanceAndEmitsTransitions(t *testing.T) {
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "write-1", ToolName: "WriteFile"}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "test-1", ToolName: "Bash", Arguments: map[string]any{"command": "go test ./..."}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(namedTool{name: "WriteFile"})
	manager.RegisterTool(namedTool{name: "Bash"})
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}

	var phases []RunPhase
	for event := range runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{}) {
		if item, ok := event.(RunPhaseEvent); ok {
			phases = append(phases, item.Phase)
		}
	}

	wantPhases := []RunPhase{PhaseExplore, PhaseImplement, PhaseVerify, PhaseFinalize}
	if len(phases) != len(wantPhases) {
		t.Fatalf("phases = %v", phases)
	}
	for index, want := range wantPhases {
		if phases[index] != want {
			t.Fatalf("phase %d = %q, want %q", index, phases[index], want)
		}
	}
	if len(client.prompts) != 3 || !strings.Contains(client.prompts[0], "exploration phase") ||
		!strings.Contains(client.prompts[1], "implementation phase") || !strings.Contains(client.prompts[2], "finalization phase") {
		t.Fatalf("prompts = %#v", client.prompts)
	}
}
