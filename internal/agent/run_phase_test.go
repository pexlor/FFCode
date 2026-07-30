package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"FFCode/internal/llm"
	"FFCode/internal/permission"
	"FFCode/internal/tool"
)

func TestRunPhaseControllerUsesEvidenceAndAllowsRework(t *testing.T) {
	controller := newRunPhaseController()
	if controller.phase() != PhaseExplore {
		t.Fatalf("initial phase = %q", controller.phase())
	}

	transition := controller.observe(phaseObservation{WorkspaceChanged: true})
	if transition.To != PhaseImplement {
		t.Fatalf("implement transition = %+v", transition)
	}
	transition = controller.observe(phaseObservation{VerificationAttempted: true})
	if transition.To != PhaseVerify {
		t.Fatalf("verify transition = %+v", transition)
	}
	transition = controller.observe(phaseObservation{WorkspaceChanged: true})
	if transition.To != PhaseImplement || transition.Reason != PhaseReasonWorkspaceChanged {
		t.Fatalf("rework transition = %+v", transition)
	}
	transition = controller.observe(phaseObservation{FinalRequested: true})
	if transition.To != PhaseFinalize || transition.Reason != PhaseReasonFinalRequested {
		t.Fatalf("final transition = %+v", transition)
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

type phaseBashTool struct{ repo string }

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

func (t phaseBashTool) Name() string        { return "Bash" }
func (t phaseBashTool) Description() string { return "phase test bash" }
func (t phaseBashTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: "Bash", Access: tool.ToolAccessExclusive, Parameters: map[string]any{"type": "object"}}
}
func (t phaseBashTool) Execute(_ context.Context, arguments map[string]any) tool.ToolResult {
	command, _ := arguments["command"].(string)
	if strings.Contains(command, "apply_patch") {
		if err := os.WriteFile(filepath.Join(t.repo, "changed.go"), []byte("package sample\n"), 0o644); err != nil {
			return tool.ToolResult{Output: err.Error(), IsError: true}
		}
	}
	return tool.ToolResult{Output: "ok"}
}

func TestAgentInjectsCurrentPhaseGuidanceAndEmitsTransitions(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "write-1", ToolName: "Bash", Arguments: map[string]any{"command": "apply_patch < patch.diff"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "test-1", ToolName: "Bash", Arguments: map[string]any{"command": "go test ./..."}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(phaseBashTool{repo: repo})
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession()
	session.Workspace = repo

	var phases []RunPhase
	for event := range runner.RunContextWithBudget(context.Background(), session, RunBudget{}) {
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
		!strings.Contains(client.prompts[1], "implementation phase") || !strings.Contains(client.prompts[2], "verification phase") {
		t.Fatalf("prompts = %#v", client.prompts)
	}
}

func TestAgentEmitsQualityWarningsBeforeTerminalEvent(t *testing.T) {
	repo := initChangeDetectorRepo(t)
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "write-1", ToolName: "Bash", Arguments: map[string]any{"command": "apply_patch < patch.diff"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(phaseBashTool{repo: repo})
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	session := testSession()
	session.Workspace = repo

	warningIndex, terminalIndex := -1, -1
	index := 0
	for event := range runner.RunContextWithBudget(context.Background(), session, RunBudget{}) {
		switch item := event.(type) {
		case QualityWarningEvent:
			if item.Code == "QG001" {
				warningIndex = index
			}
		case TurnEndEvent:
			if terminalIndex >= 0 {
				t.Fatal("multiple terminal events")
			}
			terminalIndex = index
			if item.Status != TurnCompleted || item.StopReason != StopEndTurn {
				t.Fatalf("terminal event = %+v", item)
			}
		}
		index++
	}
	if warningIndex < 0 || terminalIndex < 0 || warningIndex >= terminalIndex {
		t.Fatalf("warning index = %d, terminal index = %d", warningIndex, terminalIndex)
	}
}
