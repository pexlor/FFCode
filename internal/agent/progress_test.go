package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"MyCode/internal/llm"
	"MyCode/internal/tool"
)

func TestProgressTrackerWarnsThenBlocksUnchangedToolCall(t *testing.T) {
	tracker := newProgressTracker(DefaultProgressPolicy())
	call := llm.ToolCallComplete{ToolID: "call-1", ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md", "offset": 0}}
	result := tool.ToolResult{Output: "contents"}

	if decision := tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "workspace-a"); decision.Kind != ProgressNone {
		t.Fatalf("first decision = %+v", decision)
	}
	call.ToolID = "call-2"
	if decision := tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "workspace-a"); decision.Kind != ProgressWarning || decision.Repetition != 2 {
		t.Fatalf("second decision = %+v", decision)
	}
	call.ToolID = "call-3"
	blocked := tracker.beforeTools([]llm.ToolCallComplete{call}, "workspace-a")
	if blocked[0].Kind != ProgressToolBlocked || blocked[0].Repetition != 3 {
		t.Fatalf("blocked = %+v", blocked)
	}
}

func TestProgressTrackerAllowsRepeatedCallAfterWorkspaceChange(t *testing.T) {
	tracker := newProgressTracker(DefaultProgressPolicy())
	call := llm.ToolCallComplete{ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md"}}
	result := tool.ToolResult{Output: "contents"}
	tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "workspace-a")
	tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "workspace-a")

	if blocked := tracker.beforeTools([]llm.ToolCallComplete{call}, "workspace-b"); len(blocked) != 0 {
		t.Fatalf("blocked after workspace change: %+v", blocked)
	}
}

func TestProgressTrackerFinalizesThenStopsAfterNoProgress(t *testing.T) {
	tracker := newProgressTracker(ProgressPolicy{WarnRepeat: 2, BlockRepeat: 3, FinalizeAfter: 2, StopAfter: 3})
	call := llm.ToolCallComplete{ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md"}}
	result := tool.ToolResult{Output: "contents"}
	tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "same")
	tracker.observe([]llm.ToolCallComplete{call}, []tool.ToolResult{result}, "same")

	if decision := tracker.recordBlocked(); decision.Kind != ProgressFinalize {
		t.Fatalf("finalize decision = %+v", decision)
	}
	if decision := tracker.recordBlocked(); decision.Kind != ProgressStop {
		t.Fatalf("stop decision = %+v", decision)
	}
}

type staticFingerprinter string

func (f staticFingerprinter) Fingerprint(context.Context, string) (string, error) {
	return string(f), nil
}

type progressCountingTool struct{ calls atomic.Int32 }

func (t *progressCountingTool) Name() string        { return "ReadFile" }
func (t *progressCountingTool) Description() string { return "read" }
func (t *progressCountingTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.Name(), Parameters: map[string]any{"type": "object"}}
}
func (t *progressCountingTool) Execute(context.Context, map[string]any) tool.ToolResult {
	t.calls.Add(1)
	return tool.ToolResult{Output: "same"}
}

func TestAgentBlocksThirdUnchangedToolCall(t *testing.T) {
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "call-1", ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "call-2", ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "call-3", ToolName: "ReadFile", Arguments: map[string]any{"file_path": "README.md"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	registered := &progressCountingTool{}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	runner.ProgressFingerprinter = staticFingerprinter("unchanged")

	var warning, blocked bool
	for event := range runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxToolCalls: 10}) {
		if item, ok := event.(ProgressEvent); ok {
			warning = warning || item.Kind == ProgressWarning
			blocked = blocked || item.Kind == ProgressToolBlocked
		}
	}
	if registered.calls.Load() != 2 || !warning || !blocked {
		t.Fatalf("calls = %d, warning = %t, blocked = %t", registered.calls.Load(), warning, blocked)
	}
}

func TestAgentFinalizesThenStopsSustainedNoProgress(t *testing.T) {
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "call-1", ToolName: "ReadFile"}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "call-2", ToolName: "ReadFile"}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "call-3", ToolName: "ReadFile"}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.ToolCallComplete{ToolID: "call-4", ToolName: "ReadFile"}, llm.StreamEnd{StopReason: "tool_use"}},
	}}
	registered := &progressCountingTool{}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	runner.ProgressFingerprinter = staticFingerprinter("unchanged")
	session := testSession()

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{MaxToolCalls: 10}))
	if terminal.Status != TurnIncomplete || terminal.StopReason != StopNoProgress || terminal.ProviderReason != "no_progress" {
		t.Fatalf("terminal event = %+v", terminal)
	}
	if registered.calls.Load() != 2 {
		t.Fatalf("tool calls = %d, want 2", registered.calls.Load())
	}
	if len(client.prompts) != 4 || !strings.Contains(client.prompts[2], "Change approach") || !strings.Contains(client.prompts[3], "Finalize now") {
		t.Fatalf("prompts = %#v", client.prompts)
	}
	last := session.History[len(session.History)-1].ToolResults
	if len(last) != 1 || last[0].ToolUseID != "call-4" || !last[0].IsError {
		t.Fatalf("last tool results = %+v", last)
	}
}
