package subagent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"FFCode/internal/agent"
	"FFCode/internal/hook"
	"FFCode/internal/llm"
)

type scriptedClient struct {
	mu        sync.Mutex
	responses [][]llm.StreamEvent
	requests  []*llm.StreamRequest
}

type blockingClient struct {
	started chan struct{}
}

func (c blockingClient) Stream(request *llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	events := make(chan llm.StreamEvent)
	errs := make(chan error, 1)
	go func() {
		select {
		case c.started <- struct{}{}:
		default:
		}
		<-request.Context.Done()
		errs <- request.Context.Err()
		close(events)
		close(errs)
	}()
	return events, errs
}

type gatedClient struct {
	mu      sync.Mutex
	calls   int
	started chan int
	release chan struct{}
}

func (c *gatedClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.mu.Lock()
	c.calls++
	call := c.calls
	c.mu.Unlock()
	events := make(chan llm.StreamEvent, 2)
	errs := make(chan error)
	go func() {
		c.started <- call
		<-c.release
		events <- llm.TextStream{Text: "done"}
		events <- llm.StreamEnd{StopReason: "end_turn"}
		close(events)
		close(errs)
	}()
	return events, errs
}

func (c *scriptedClient) Stream(request *llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.mu.Lock()
	index := len(c.requests)
	c.requests = append(c.requests, request)
	var response []llm.StreamEvent
	if index < len(c.responses) {
		response = c.responses[index]
	}
	c.mu.Unlock()
	events := make(chan llm.StreamEvent, len(response))
	errs := make(chan error, 1)
	for _, event := range response {
		events <- event
	}
	close(events)
	close(errs)
	return events, errs
}

func TestManagerDelegatesReadOnlyTask(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{responses: [][]llm.StreamEvent{
		{
			llm.ToolCallComplete{ToolID: "read-1", ToolName: "ReadFile", Arguments: map[string]any{"file_path": path}},
			llm.StreamEnd{StopReason: "tool_use", Usage: llm.UsageInfo{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}},
		},
		{
			llm.TextStream{Text: "The file contains evidence."},
			llm.StreamEnd{StopReason: "end_turn", Usage: llm.UsageInfo{InputTokens: 5, OutputTokens: 4, TotalTokens: 9}},
		},
	}}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{
		MaxDuration: time.Minute, MaxInputTokens: 100, MaxOutputTokens: 100, MaxToolCalls: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Delegate(ctx, Request{ParentSessionID: "parent-1", Workspace: workspace, Task: "inspect sample", Budget: agent.RunBudget{
		MaxDuration: time.Minute, MaxInputTokens: 50, MaxOutputTokens: 20, MaxToolCalls: 10,
	}})
	if result.Status != StatusCompleted || result.Summary != "The file contains evidence." {
		t.Fatalf("result = %+v", result)
	}
	if len(result.FilesRead) != 1 || result.FilesRead[0] != path {
		t.Fatalf("files read = %v", result.FilesRead)
	}
	if len(result.Evidence) == 0 || result.Evidence[0].Source != path {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
	if result.Usage.InputTokens != 15 || result.Usage.OutputTokens != 7 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if len(client.requests) != 2 || client.requests[0].SystemPrompt == "" {
		t.Fatalf("requests = %+v", client.requests)
	}
	for _, schema := range client.requests[0].Tools {
		if schema.Name == "delegate_task" || schema.Name == "WriteFile" || schema.Name == "Bash" {
			t.Fatalf("child received forbidden tool %q", schema.Name)
		}
	}
}

func TestManagerRejectsCallsBeyondPerRunLimit(t *testing.T) {
	client := &scriptedClient{responses: [][]llm.StreamEvent{{llm.TextStream{Text: "one"}, llm.StreamEnd{StopReason: "end_turn"}}}}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	first := manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: t.TempDir(), Task: "first"})
	if first.Status != StatusCompleted {
		t.Fatalf("first = %+v", first)
	}
	second := manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: t.TempDir(), Task: "second"})
	if second.Status != StatusRejected {
		t.Fatalf("second = %+v", second)
	}
}

func TestManagerPublishesIdentifiedLifecycleEvents(t *testing.T) {
	client := &scriptedClient{responses: [][]llm.StreamEvent{{llm.TextStream{Text: "done"}, llm.StreamEnd{StopReason: "end_turn"}}}}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	var events []agent.AgentEvent
	ctx := agent.WithAgentEventSink(context.Background(), func(event agent.AgentEvent) bool {
		events = append(events, event)
		return true
	})
	ctx, err = agent.NewChildRuntimeContext(ctx, agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Delegate(ctx, Request{ParentSessionID: "parent-1", Workspace: t.TempDir(), Task: "inspect"})
	if result.Status != StatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	if len(events) < 3 {
		t.Fatalf("events = %#v", events)
	}
	start, ok := events[0].(agent.SubagentStartEvent)
	if !ok || start.SubagentID != result.SubagentID || start.ParentSessionID != "parent-1" {
		t.Fatalf("start = %#v", events[0])
	}
	stop, ok := events[len(events)-1].(agent.SubagentStopEvent)
	if !ok || stop.SubagentID != result.SubagentID || stop.Status != string(StatusCompleted) {
		t.Fatalf("stop = %#v", events[len(events)-1])
	}
	for _, event := range events[1 : len(events)-1] {
		wrapped, ok := event.(agent.SubagentEvent)
		if !ok || wrapped.SubagentID != result.SubagentID {
			t.Fatalf("wrapped = %#v", event)
		}
	}
}

func TestManagerClassifiesChildDeadlineAsBudgetExceeded(t *testing.T) {
	manager, err := NewManager(blockingClient{started: make(chan struct{}, 1)}, nil, Config{
		MaxConcurrent: 1,
		MaxPerRun:     1,
		DefaultBudget: agent.RunBudget{MaxDuration: 10 * time.Millisecond, MaxInputTokens: 10, MaxOutputTokens: 10, MaxToolCalls: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{MaxDuration: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: t.TempDir(), Task: "wait"})
	if result.Status != StatusBudgetExceeded || result.StopReason != agent.StopDeadlineExceeded {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagerClassifiesParentCancellationAsCanceled(t *testing.T) {
	client := blockingClient{started: make(chan struct{}, 1)}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	ctx, err := agent.NewChildRuntimeContext(parent, agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	resultCh := make(chan Result, 1)
	go func() {
		resultCh <- manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: t.TempDir(), Task: "wait"})
	}()
	<-client.started
	cancel()
	result := <-resultCh
	if result.Status != StatusCanceled {
		t.Fatalf("result = %+v", result)
	}
}

func TestManagerDoesNotRunToolHooksInsideReadOnlyChild(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "sample.txt")
	if err := os.WriteFile(path, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &scriptedClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "read-1", ToolName: "ReadFile", Arguments: map[string]any{"file_path": path}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.TextStream{Text: "done"}, llm.StreamEnd{StopReason: "end_turn"}},
	}}
	dispatcher := hook.New(hook.DefaultConfig())
	toolHooks := 0
	lifecycleHooks := 0
	if err := dispatcher.Register(hook.EventPreToolUse, func(hook.Input) { toolHooks++ }); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.EventSubagentStart, func(hook.Input) { lifecycleHooks++ }); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.EventSubagentStop, func(hook.Input) { lifecycleHooks++ }); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(client, dispatcher, Config{MaxConcurrent: 1, MaxPerRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: workspace, Task: "read"})
	if result.Status != StatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	if toolHooks != 0 || lifecycleHooks != 2 {
		t.Fatalf("tool hooks = %d, lifecycle hooks = %d", toolHooks, lifecycleHooks)
	}
}

func TestManagerUsesWorkspaceAsDefaultEvidenceSource(t *testing.T) {
	workspace := t.TempDir()
	client := &scriptedClient{responses: [][]llm.StreamEvent{
		{llm.ToolCallComplete{ToolID: "glob-1", ToolName: "Glob", Arguments: map[string]any{"pattern": "**/*.go"}}, llm.StreamEnd{StopReason: "tool_use"}},
		{llm.TextStream{Text: "done"}, llm.StreamEnd{StopReason: "end_turn"}},
	}}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	result := manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: workspace, Task: "list go files"})
	if result.Status != StatusCompleted {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Source != workspace {
		t.Fatalf("evidence = %+v", result.Evidence)
	}
}

func TestManagerEnforcesConcurrentLimit(t *testing.T) {
	client := &gatedClient{started: make(chan int, 2), release: make(chan struct{})}
	manager, err := NewManager(client, nil, Config{MaxConcurrent: 1, MaxPerRun: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := agent.NewChildRuntimeContext(context.Background(), agent.RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan Result, 2)
	workspace := t.TempDir()
	for index := range 2 {
		go func(index int) {
			results <- manager.Delegate(ctx, Request{ParentSessionID: "parent", Workspace: workspace, Task: fmt.Sprintf("task-%d", index)})
		}(index)
	}
	<-client.started
	select {
	case call := <-client.started:
		t.Fatalf("call %d started before a concurrency slot was released", call)
	case <-time.After(20 * time.Millisecond):
	}
	client.release <- struct{}{}
	if result := <-results; result.Status != StatusCompleted {
		t.Fatalf("first result = %+v", result)
	}
	select {
	case <-client.started:
	case <-time.After(time.Second):
		t.Fatal("queued subagent did not start")
	}
	client.release <- struct{}{}
	if result := <-results; result.Status != StatusCompleted {
		t.Fatalf("second result = %+v", result)
	}
}
