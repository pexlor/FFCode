package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	contextmanager "MyCode/internal/context"
	"MyCode/internal/conversation"
	"MyCode/internal/llm"
	"MyCode/internal/tool"
)

type budgetClient struct {
	events []llm.StreamEvent
	block  bool
}

func (c budgetClient) Stream(request *llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	events := make(chan llm.StreamEvent, len(c.events))
	errors := make(chan error)
	if c.block {
		return events, errors
	}
	for _, event := range c.events {
		events <- event
	}
	close(events)
	close(errors)
	return events, errors
}

type countingTool struct{ calls atomic.Int32 }

func (t *countingTool) Name() string        { return "counting" }
func (t *countingTool) Description() string { return "counts executions" }
func (t *countingTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.Name(), Parameters: map[string]any{"type": "object"}}
}
func (t *countingTool) Execute(context.Context, map[string]any) tool.ToolResult {
	t.calls.Add(1)
	return tool.ToolResult{Output: "ok"}
}

func TestRunBudgetStopsBeforeToolsWhenTokenLimitIsExceeded(t *testing.T) {
	client := budgetClient{events: []llm.StreamEvent{
		llm.ToolCallComplete{ToolID: "call-1", ToolName: "counting"},
		llm.StreamEnd{StopReason: "tool_use", Usage: llm.UsageInfo{InputTokens: 11, OutputTokens: 2, TotalTokens: 13}},
	}}
	registered := &countingTool{}
	manager := tool.NewToolsManager()
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}

	event := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxInputTokens: 10}))

	if registered.calls.Load() != 0 {
		t.Fatalf("tool calls = %d, want 0", registered.calls.Load())
	}
	if event.Status != TurnIncomplete || event.StopReason != StopBudgetExceeded {
		t.Fatalf("terminal event = %+v", event)
	}
	if event.Usage.TotalTokens != 13 {
		t.Fatalf("usage = %+v", event.Usage)
	}
}

func TestRunBudgetRejectsToolBatchWithoutPartialExecution(t *testing.T) {
	client := budgetClient{events: []llm.StreamEvent{
		llm.ToolCallComplete{ToolID: "call-1", ToolName: "counting"},
		llm.ToolCallComplete{ToolID: "call-2", ToolName: "counting"},
		llm.StreamEnd{StopReason: "tool_use"},
	}}
	registered := &countingTool{}
	manager := tool.NewToolsManager()
	manager.RegisterTool(registered)
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}

	event := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxToolCalls: 1}))

	if registered.calls.Load() != 0 {
		t.Fatalf("tool calls = %d, want 0", registered.calls.Load())
	}
	if event.Status != TurnIncomplete || event.StopReason != StopBudgetExceeded {
		t.Fatalf("terminal event = %+v", event)
	}
}

func TestRunBudgetDeadlineCancelsBlockedProvider(t *testing.T) {
	runner, err := NewAgent(context.Background(), budgetClient{block: true}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	event := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxDuration: 20 * time.Millisecond}))

	if event.Status != TurnFailed || event.StopReason != StopDeadlineExceeded {
		t.Fatalf("terminal event = %+v", event)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("deadline took %s", elapsed)
	}
}

func testSession() *contextmanager.ConversationContext {
	return &contextmanager.ConversationContext{SessionID: "session-test", History: []conversation.Message{{Role: conversation.USER, Content: "test"}}}
}

func terminalEvent(events <-chan AgentEvent) TurnEndEvent {
	var terminal TurnEndEvent
	for event := range events {
		if item, ok := event.(TurnEndEvent); ok {
			terminal = item
		}
	}
	return terminal
}
