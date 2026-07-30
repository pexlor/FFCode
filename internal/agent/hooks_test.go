package agent

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	contextmanager "FFCode/internal/context"
	"FFCode/internal/conversation"
	"FFCode/internal/hook"
	"FFCode/internal/llm"
	"FFCode/internal/tool"
)

type lifecycleSessionStore struct {
	metadata conversation.SessionMetadata
}

func (s *lifecycleSessionStore) CreateSession(context.Context, conversation.SessionMetadata) error {
	return nil
}

func (s *lifecycleSessionStore) GetSession(_ context.Context, id string) (conversation.SessionMetadata, error) {
	if id != s.metadata.ID {
		return conversation.SessionMetadata{}, conversation.ErrStoreSessionNotFound
	}
	return s.metadata, nil
}

func (s *lifecycleSessionStore) ListSessions(context.Context, string, int) ([]conversation.SessionMetadata, error) {
	return []conversation.SessionMetadata{s.metadata}, nil
}

func (s *lifecycleSessionStore) RenameSession(context.Context, string, string) error { return nil }
func (s *lifecycleSessionStore) DeleteSession(context.Context, string) error         { return nil }
func (s *lifecycleSessionStore) ListMessages(context.Context, string) ([]conversation.StoredMessage, error) {
	return nil, nil
}

func TestSessionStartRunsOncePerTransitionAcrossServiceAndAgent(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventSessionStart, func(hook.Input) {
		calls.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	store := &lifecycleSessionStore{metadata: conversation.SessionMetadata{
		ID: "session-a", Title: "A", Workspace: "/workspace", CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}}
	service, err := conversation.NewService(store, "/workspace", conversation.SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	service.SetHookDispatcher(dispatcher)
	runner := &Agent{Hooks: dispatcher}

	dispatchTransition := func(session *conversation.Session, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		conversationContext, contextErr := contextmanager.ContextFromSession(context.Background(), session)
		if contextErr != nil {
			t.Fatal(contextErr)
		}
		if _, err := runner.dispatchRunStartHooks(context.Background(), conversationContext); err != nil {
			t.Fatal(err)
		}
	}
	dispatchTransition(service.Resume(context.Background(), "session-a"))
	dispatchTransition(service.New(context.Background(), "B"))
	dispatchTransition(service.Resume(context.Background(), "session-a"))

	if got := calls.Load(); got != 3 {
		t.Fatalf("session_start calls = %d, want 3", got)
	}
}

func TestAgentDispatchesStartPromptAndStopLifecycles(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var mu sync.Mutex
	var events []hook.Event
	register := func(event hook.Event) {
		t.Helper()
		if err := dispatcher.Register(event, func(_ context.Context, input hook.Input) (hook.Output, error) {
			if input.SessionID != "session-test" {
				t.Errorf("%s session = %q", event, input.SessionID)
			}
			mu.Lock()
			events = append(events, event)
			mu.Unlock()
			return hook.Output{}, nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	register(hook.EventSessionStart)
	register(hook.EventUserPromptSubmit)
	register(hook.EventStop)
	runner, err := NewAgent(context.Background(), budgetClient{events: []llm.StreamEvent{llm.StreamEnd{StopReason: "end_turn"}}}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetHookDispatcher(dispatcher)
	session := testSession()
	first := terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{}))
	second := terminalEvent(runner.RunContextWithBudget(context.Background(), session, RunBudget{}))
	if first.Status != TurnCompleted || second.Status != TurnCompleted {
		t.Fatalf("terminals = %+v, %+v", first, second)
	}
	mu.Lock()
	defer mu.Unlock()
	want := []hook.Event{hook.EventSessionStart, hook.EventUserPromptSubmit, hook.EventStop, hook.EventStop}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("hook events = %v, want %v", events, want)
	}
}

func TestAgentStopHookRunsWithDetachedContextAfterDeadline(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventStop, func(ctx context.Context, input hook.Input) (hook.Output, error) {
		calls.Add(1)
		if ctx.Err() != nil {
			t.Errorf("stop context remained cancelled: %v", ctx.Err())
		}
		if input.Reason != string(StopDeadlineExceeded) {
			t.Errorf("stop reason = %q", input.Reason)
		}
		return hook.Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := NewAgent(context.Background(), budgetClient{block: true}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetHookDispatcher(dispatcher)
	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxDuration: 20 * time.Millisecond}))
	if terminal.StopReason != StopDeadlineExceeded || calls.Load() != 1 {
		t.Fatalf("terminal=%+v stop calls=%d", terminal, calls.Load())
	}
}

type panicClient struct{}

func (panicClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	panic("provider panic")
}

func TestAgentRecoversPanicsAndEmitsOneFailedTerminalEvent(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var stopCalls atomic.Int32
	if err := dispatcher.Register(hook.EventStop, func(input hook.Input) {
		stopCalls.Add(1)
		if !strings.Contains(input.Reason, string(StopAgentError)) {
			t.Errorf("stop reason = %q", input.Reason)
		}
	}); err != nil {
		t.Fatal(err)
	}
	runner, err := NewAgent(context.Background(), panicClient{}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetHookDispatcher(dispatcher)

	var terminals []TurnEndEvent
	for event := range runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{}) {
		if terminal, ok := event.(TurnEndEvent); ok {
			terminals = append(terminals, terminal)
		}
	}

	if len(terminals) != 1 {
		t.Fatalf("terminal events = %+v", terminals)
	}
	terminal := terminals[0]
	if terminal.Status != TurnFailed || terminal.StopReason != StopAgentError || terminal.Err == nil || !strings.Contains(terminal.Err.Error(), "provider panic") {
		t.Fatalf("terminal = %+v", terminal)
	}
	if stopCalls.Load() != 1 {
		t.Fatalf("stop calls = %d, want 1", stopCalls.Load())
	}
}

func TestFailClosedStopHookChangesTerminalOutcome(t *testing.T) {
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventStop: hook.FailureClosed},
	})
	if err := dispatcher.Register(hook.EventStop, func(hook.Input) error { return errors.New("stop failed") }); err != nil {
		t.Fatal(err)
	}
	runner, err := NewAgent(context.Background(), budgetClient{events: []llm.StreamEvent{llm.StreamEnd{StopReason: "end_turn"}}}, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.SetHookDispatcher(dispatcher)
	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{}))
	if terminal.Status != TurnFailed || terminal.StopReason != StopAgentError || terminal.Err == nil {
		t.Fatalf("terminal = %+v", terminal)
	}
}

func TestRunSubagentAlwaysPairsLifecycleHooks(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var order []string
	if err := dispatcher.Register(hook.EventSubagentStart, func(hook.Input) hook.Output {
		order = append(order, "start")
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.EventSubagentStop, func(input hook.Input) hook.Output {
		order = append(order, "stop")
		if !input.IsError || input.Reason != "subagent failed" {
			t.Errorf("stop input = %+v", input)
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Agent{Hooks: dispatcher}
	wantErr := errors.New("subagent failed")
	err := runner.RunSubagent(context.Background(), hook.Input{Metadata: map[string]any{"agent_id": "child-1"}}, func(context.Context) error {
		order = append(order, "run")
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v", err)
	}
	if !reflect.DeepEqual(order, []string{"start", "run", "stop"}) {
		t.Fatalf("order = %v", order)
	}
}

func TestRunSubagentStopUsesDetachedContextAfterCancellation(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var stopCalls atomic.Int32
	if err := dispatcher.Register(hook.EventSubagentStop, func(ctx context.Context, input hook.Input) hook.Output {
		stopCalls.Add(1)
		if ctx.Err() != nil {
			t.Errorf("subagent_stop context remained canceled: %v", ctx.Err())
		}
		if !input.IsError || input.Reason != context.Canceled.Error() {
			t.Errorf("subagent_stop input = %+v", input)
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Agent{Hooks: dispatcher}
	ctx, cancel := context.WithCancel(context.Background())
	err := runner.RunSubagent(ctx, hook.Input{}, func(ctx context.Context) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || stopCalls.Load() != 1 {
		t.Fatalf("error=%v stop calls=%d", err, stopCalls.Load())
	}
}

func TestRunSubagentStopRunsBeforePanicIsRethrown(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var stopInput hook.Input
	if err := dispatcher.Register(hook.EventSubagentStop, func(input hook.Input) {
		stopInput = input
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Agent{Hooks: dispatcher}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = runner.RunSubagent(context.Background(), hook.Input{}, func(context.Context) error {
			panic("boom")
		})
	}()
	if recovered != "boom" {
		t.Fatalf("recovered panic = %#v", recovered)
	}
	if !stopInput.IsError || stopInput.Reason != "panic: boom" {
		t.Fatalf("subagent_stop input = %+v", stopInput)
	}
}

func TestRunSubagentPanicIncludesFailClosedStopHookFailure(t *testing.T) {
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventSubagentStop: hook.FailureClosed},
	})
	panicErr := errors.New("subagent panicked")
	stopErr := errors.New("subagent stop failed")
	if err := dispatcher.Register(hook.EventSubagentStop, func(hook.Input) error {
		return stopErr
	}); err != nil {
		t.Fatal(err)
	}
	runner := &Agent{Hooks: dispatcher}

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = runner.RunSubagent(context.Background(), hook.Input{}, func(context.Context) error {
			panic(panicErr)
		})
	}()
	recoveredErr, ok := recovered.(error)
	if !ok {
		t.Fatalf("recovered panic = %#v, want error", recovered)
	}
	if !errors.Is(recoveredErr, panicErr) {
		t.Fatalf("recovered panic = %v, want panic cause %v", recoveredErr, panicErr)
	}
	if !errors.Is(recoveredErr, stopErr) {
		t.Fatalf("recovered panic = %v, want stop-hook cause %v", recoveredErr, stopErr)
	}
}

func TestAgentSurfacesPostToolHookFailureWithoutRelabelingToolResult(t *testing.T) {
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
	})
	wantErr := errors.New("audit unavailable")
	if err := dispatcher.Register(hook.EventPostToolUse, func(hook.Input) error { return wantErr }); err != nil {
		t.Fatal(err)
	}
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(stubTool{})
	manager.SetHookDispatcher(dispatcher)
	runner := &Agent{toolManager: manager}
	events := make(chan AgentEvent, 4)

	results, err := runner.executeToolsWithBlocked(context.Background(), []llm.ToolCallComplete{{
		ToolID: "call-1", ToolName: "stub",
	}}, nil, events)
	if !errors.Is(err, wantErr) {
		t.Fatalf("post hook error = %v", err)
	}
	if len(results) != 1 || results[0].IsError {
		t.Fatalf("tool results = %+v", results)
	}
}

type cancelingHookTool struct {
	started chan struct{}
}

func (t cancelingHookTool) Name() string        { return "cancel-hook-tool" }
func (t cancelingHookTool) Description() string { return "waits for cancellation" }
func (t cancelingHookTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.Name(), Access: tool.ToolAccessWrite, Parameters: map[string]any{"type": "object"}}
}
func (t cancelingHookTool) Execute(ctx context.Context, _ map[string]any) tool.ToolResult {
	close(t.started)
	<-ctx.Done()
	return tool.ToolResult{Output: "tool observed cancellation", IsError: true}
}

type contextAwareHookCheckpointStore struct{}

func (contextAwareHookCheckpointStore) Load(context.Context, string) (RunCheckpoint, error) {
	return RunCheckpoint{}, ErrCheckpointNotFound
}

func (contextAwareHookCheckpointStore) Save(ctx context.Context, _ RunCheckpoint) error {
	return ctx.Err()
}

func TestCancelledTurnPreservesPostHookFailureWhenCheckpointSaveAlsoFails(t *testing.T) {
	started := make(chan struct{})
	manager := tool.NewToolsManager()
	manager.SetPermissionManager(allowPermissionManager{})
	manager.RegisterTool(cancelingHookTool{started: started})
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
	})
	wantErr := errors.New("post hook audit failed")
	var postFinished atomic.Bool
	if err := dispatcher.Register(hook.EventPostToolUse, func(hook.Input) error {
		postFinished.Store(true)
		return wantErr
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.EventStop, func(hook.Input) {
		if !postFinished.Load() {
			t.Error("stop hook ran before post_tool_use completed")
		}
	}); err != nil {
		t.Fatal(err)
	}
	client := budgetClient{events: []llm.StreamEvent{
		llm.ToolCallComplete{ToolID: "call-cancel", ToolName: "cancel-hook-tool"},
		llm.StreamEnd{StopReason: "tool_use"},
	}}
	runner, err := NewAgent(context.Background(), client, manager)
	if err != nil {
		t.Fatal(err)
	}
	runner.SetHookDispatcher(dispatcher)
	runner.CheckpointStore = contextAwareHookCheckpointStore{}

	ctx, cancel := context.WithCancel(context.Background())
	terminal := make(chan TurnEndEvent, 1)
	go func() {
		terminal <- terminalEvent(runner.RunContextWithBudget(ctx, testSession(), RunBudget{}))
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}

	select {
	case event := <-terminal:
		if !errors.Is(event.Err, context.Canceled) || !errors.Is(event.Err, wantErr) {
			t.Fatalf("terminal error = %v, want cancellation and post hook failure", event.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("agent did not terminate after cancellation")
	}
}
