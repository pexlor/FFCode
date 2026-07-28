package agent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"MyCode/internal/conversation"
	"MyCode/internal/llm"
	"MyCode/internal/tool"
)

type memoryCheckpointStore struct {
	mu         sync.Mutex
	checkpoint RunCheckpoint
	hasValue   bool
	boundaries []CheckpointBoundary
}

type checkpointCountingTool struct{ calls atomic.Int32 }

func (t *checkpointCountingTool) Name() string        { return "WriteFile" }
func (t *checkpointCountingTool) Description() string { return "write" }
func (t *checkpointCountingTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.Name(), Access: tool.ToolAccessWrite, Parameters: map[string]any{"type": "object"}}
}
func (t *checkpointCountingTool) Execute(context.Context, map[string]any) tool.ToolResult {
	t.calls.Add(1)
	return tool.ToolResult{Output: "written"}
}

func (s *memoryCheckpointStore) Load(context.Context, string) (RunCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.hasValue {
		return RunCheckpoint{}, ErrCheckpointNotFound
	}
	return s.checkpoint, nil
}

func (s *memoryCheckpointStore) Save(_ context.Context, checkpoint RunCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint = checkpoint
	s.hasValue = true
	s.boundaries = append(s.boundaries, checkpoint.Boundary)
	return nil
}

func (s *memoryCheckpointStore) latest() RunCheckpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkpoint
}

func TestAgentSavesInterruptedCheckpointAfterDeadline(t *testing.T) {
	store := &memoryCheckpointStore{}
	runner, err := NewAgent(context.Background(), budgetClient{block: true}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.CheckpointStore = store
	session := testSession()

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{MaxDuration: 20 * time.Millisecond}))
	checkpoint := store.latest()

	if terminal.StopReason != StopDeadlineExceeded {
		t.Fatalf("terminal = %+v", terminal)
	}
	if checkpoint.Boundary != CheckpointInterrupted || checkpoint.Completed || checkpoint.SessionID != session.ID {
		t.Fatalf("checkpoint = %+v", checkpoint)
	}
	if len(checkpoint.History) != len(session.History) {
		t.Fatalf("checkpoint history = %+v, session history = %+v", checkpoint.History, session.History)
	}
}

func TestAgentRecoversPendingCallsWithoutReplayingAndWarnsOnWorkspaceChange(t *testing.T) {
	checkpointHistory := []conversation.Message{
		{Role: conversation.USER, Content: "change a file"},
		{Role: conversation.ASSISTANT, ToolUses: []conversation.ToolUseBlock{{ToolUseID: "write-1", ToolName: "WriteFile"}}},
	}
	store := &memoryCheckpointStore{hasValue: true, checkpoint: RunCheckpoint{
		Version: CheckpointFormatVersion, SessionID: "session-test", Boundary: CheckpointModel,
		WorkspaceFingerprint: "before", History: checkpointHistory,
		PendingToolUses: []conversation.ToolUseBlock{{ToolUseID: "write-1", ToolName: "WriteFile"}},
	}}
	client := &phaseClient{responses: [][]llm.StreamEvent{{llm.StreamEnd{StopReason: "end_turn"}}}}
	registered := &checkpointCountingTool{}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	runner.CheckpointStore = store
	runner.ProgressFingerprinter = staticFingerprinter("after")
	session := testSession()
	session.Workspace = t.TempDir()

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{}))

	if terminal.Status != TurnCompleted || registered.calls.Load() != 0 {
		t.Fatalf("terminal = %+v, tool calls = %d", terminal, registered.calls.Load())
	}
	if len(client.prompts) != 1 || !strings.Contains(client.prompts[0], "workspace changed") || !strings.Contains(client.prompts[0], "not replay") {
		t.Fatalf("recovery prompt = %#v", client.prompts)
	}
	results := session.History[2].ToolResults
	if len(results) != 1 || results[0].ToolUseID != "write-1" || !results[0].IsError {
		t.Fatalf("recovered results = %+v", results)
	}
}

func TestAgentSavesEveryCommittedExecutionBoundary(t *testing.T) {
	store := &memoryCheckpointStore{}
	client := &phaseClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "write-1", ToolName: "WriteFile"}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.StreamEnd{StopReason: "end_turn"}},
	}}
	registered := &checkpointCountingTool{}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	runner.CheckpointStore = store
	runner.ProgressFingerprinter = staticFingerprinter("same")

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{}))

	store.mu.Lock()
	boundaries := append([]CheckpointBoundary(nil), store.boundaries...)
	store.mu.Unlock()
	want := []CheckpointBoundary{CheckpointModel, CheckpointTools, CheckpointCompleted}
	if terminal.Status != TurnCompleted || registered.calls.Load() != 1 || len(boundaries) != len(want) {
		t.Fatalf("terminal=%+v calls=%d boundaries=%v", terminal, registered.calls.Load(), boundaries)
	}
	for index := range want {
		if boundaries[index] != want[index] {
			t.Fatalf("boundary %d = %q, want %q", index, boundaries[index], want[index])
		}
	}
}

func TestRecoveryPreservesCompletedCallsAndOnlyReconcilesPendingCalls(t *testing.T) {
	history := []conversation.Message{
		{Role: conversation.USER, Content: "change files"},
		{Role: conversation.ASSISTANT, ToolUses: []conversation.ToolUseBlock{
			{ToolUseID: "done-1", ToolName: "WriteFile"},
			{ToolUseID: "pending-1", ToolName: "WriteFile"},
		}},
		{Role: conversation.TOOL, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "done-1", Content: "wrote file"}}},
	}
	store := &memoryCheckpointStore{hasValue: true, checkpoint: RunCheckpoint{
		Version: CheckpointFormatVersion, SessionID: "session-test", Boundary: CheckpointTools,
		WorkspaceFingerprint: "same", History: history,
		PendingToolUses: history[1].ToolUses, CompletedToolUseIDs: []string{"done-1"},
	}}
	client := &phaseClient{responses: [][]llm.StreamEvent{{llm.StreamEnd{StopReason: "end_turn"}}}}
	runner, err := NewAgent(context.Background(), client, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.CheckpointStore = store
	runner.ProgressFingerprinter = staticFingerprinter("same")
	session := testSession()

	_ = terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{}))

	var recovered []conversation.ToolResultBlock
	for _, message := range session.History {
		for _, result := range message.ToolResults {
			if strings.Contains(result.Content, "not replayed") {
				recovered = append(recovered, result)
			}
		}
	}
	if len(recovered) != 1 || recovered[0].ToolUseID != "pending-1" {
		t.Fatalf("recovered pending results = %+v", recovered)
	}
}

func TestCheckpointLoadErrorStopsRun(t *testing.T) {
	runner, err := NewAgent(context.Background(), budgetClient{}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.CheckpointStore = failingCheckpointStore{}
	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{}))
	if terminal.Status != TurnFailed || terminal.Err == nil {
		t.Fatalf("terminal = %+v", terminal)
	}
}

type failingCheckpointStore struct{}

func (failingCheckpointStore) Load(context.Context, string) (RunCheckpoint, error) {
	return RunCheckpoint{}, errors.New("load failed")
}
func (failingCheckpointStore) Save(context.Context, RunCheckpoint) error { return nil }
