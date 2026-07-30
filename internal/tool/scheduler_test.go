package tool

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"FFCode/internal/hook"
	"FFCode/internal/permission"
)

type allowAllPermissions struct{}

func (allowAllPermissions) Authorize(context.Context, permission.PermissionRequest) (permission.PermissionResult, error) {
	return permission.PermissionResult{Decision: permission.Allow}, nil
}

type scheduledTool struct {
	name    string
	access  ToolAccess
	execute func(context.Context) ToolResult
}

func (t scheduledTool) Name() string        { return t.name }
func (t scheduledTool) Description() string { return t.name }
func (t scheduledTool) Schema() *ToolSchema {
	return &ToolSchema{Name: t.name, Access: t.access, Parameters: map[string]any{"type": "object"}}
}
func (t scheduledTool) Execute(ctx context.Context, _ map[string]any) ToolResult {
	return t.execute(ctx)
}

func TestExecuteBatchOverlapsAdjacentReadsAndPreservesResultOrder(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	started := make(chan string, 2)
	release := make(chan struct{})
	for _, name := range []string{"read-a", "read-b"} {
		name := name
		manager.RegisterTool(scheduledTool{name: name, access: ToolAccessRead, execute: func(context.Context) ToolResult {
			started <- name
			<-release
			return ToolResult{Output: name}
		}})
	}

	done := make(chan []ToolResult, 1)
	go func() {
		done <- manager.ExecuteBatch(context.Background(), []Invocation{{ID: "1", Name: "read-a"}, {ID: "2", Name: "read-b"}})
	}()

	waitForStarts(t, started, 2)
	close(release)
	results := <-done
	want := []ToolResult{{Output: "read-a"}, {Output: "read-b"}}
	if len(results) != len(want) || results[0] != want[0] || results[1] != want[1] {
		t.Fatalf("results = %+v, want %+v", results, want)
	}
}

func TestExecuteBatchSerializesWritesBetweenReadStages(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	var mu sync.Mutex
	active := 0
	readStage := 0
	writeStarted := false
	secondReadStarted := false
	readRelease := make(chan struct{})
	writeRelease := make(chan struct{})
	firstReadsStarted := make(chan struct{}, 2)
	writeEntered := make(chan struct{}, 1)
	secondReadEntered := make(chan struct{}, 1)

	read := func(name string, second bool) scheduledTool {
		return scheduledTool{name: name, access: ToolAccessRead, execute: func(context.Context) ToolResult {
			mu.Lock()
			active++
			if second {
				secondReadStarted = true
				secondReadEntered <- struct{}{}
			} else {
				readStage++
				firstReadsStarted <- struct{}{}
			}
			mu.Unlock()
			if second {
				mu.Lock()
				active--
				mu.Unlock()
				return ToolResult{Output: name}
			}
			<-readRelease
			mu.Lock()
			active--
			mu.Unlock()
			return ToolResult{Output: name}
		}}
	}
	manager.RegisterTool(read("read-a", false))
	manager.RegisterTool(read("read-b", false))
	manager.RegisterTool(scheduledTool{name: "write", access: ToolAccessWrite, execute: func(context.Context) ToolResult {
		mu.Lock()
		if active != 0 || readStage != 2 {
			t.Errorf("write overlapped reads: active=%d readStage=%d", active, readStage)
		}
		active++
		writeStarted = true
		writeEntered <- struct{}{}
		mu.Unlock()
		<-writeRelease
		mu.Lock()
		active--
		mu.Unlock()
		return ToolResult{Output: "write"}
	}})
	manager.RegisterTool(read("read-c", true))

	done := make(chan []ToolResult, 1)
	go func() {
		done <- manager.ExecuteBatch(context.Background(), []Invocation{
			{ID: "1", Name: "read-a"}, {ID: "2", Name: "read-b"}, {ID: "3", Name: "write"}, {ID: "4", Name: "read-c"},
		})
	}()
	waitForSignals(t, firstReadsStarted, 2)
	assertNotSignaled(t, writeEntered)
	close(readRelease)
	waitForSignals(t, writeEntered, 1)
	assertNotSignaled(t, secondReadEntered)
	close(writeRelease)
	waitForSignals(t, secondReadEntered, 1)
	results := <-done

	mu.Lock()
	defer mu.Unlock()
	if !writeStarted || !secondReadStarted {
		t.Fatalf("writeStarted=%t secondReadStarted=%t", writeStarted, secondReadStarted)
	}
	want := []string{"read-a", "read-b", "write", "read-c"}
	for index, output := range want {
		if results[index].Output != output {
			t.Fatalf("result %d = %+v, want output %q", index, results[index], output)
		}
	}
}

func TestExecuteBatchDrainsStartedInvocationAfterCancellation(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
	})
	manager.SetHookDispatcher(dispatcher)

	started := make(chan struct{})
	cancellationObserved := make(chan struct{})
	releaseTool := make(chan struct{})
	manager.RegisterTool(scheduledTool{name: "started-write", access: ToolAccessWrite, execute: func(ctx context.Context) ToolResult {
		close(started)
		<-ctx.Done()
		close(cancellationObserved)
		<-releaseTool
		return ToolResult{Output: "started tool stopped after cancellation", IsError: true}
	}})
	laterStarted := make(chan struct{}, 1)
	manager.RegisterTool(scheduledTool{name: "later-read", access: ToolAccessRead, execute: func(context.Context) ToolResult {
		laterStarted <- struct{}{}
		return ToolResult{Output: "later tool ran"}
	}})

	wantHookErr := errors.New("post hook audit failed")
	postFinished := make(chan struct{})
	if err := dispatcher.Register(hook.EventPostToolUse, func(input hook.Input) error {
		if input.ToolName != "started-write" {
			return nil
		}
		close(postFinished)
		return wantHookErr
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []ToolResult, 1)
	go func() {
		done <- manager.ExecuteBatch(ctx, []Invocation{
			{ID: "started", Name: "started-write"},
			{ID: "later", Name: "later-read"},
		})
	}()

	waitForSignals(t, started, 1)
	cancel()
	waitForSignals(t, cancellationObserved, 1)

	var results []ToolResult
	returnedBeforeToolFinished := false
	select {
	case results = <-done:
		returnedBeforeToolFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseTool)
	waitForSignals(t, postFinished, 1)
	if results == nil {
		select {
		case results = <-done:
		case <-time.After(time.Second):
			t.Fatal("batch did not finish after the started tool and post hook completed")
		}
	}

	if returnedBeforeToolFinished {
		t.Errorf("ExecuteBatch returned before the started invocation's post hook completed: %+v", results)
	}
	if len(results) != 2 {
		t.Fatalf("results = %+v, want two entries", results)
	}
	if !errors.Is(results[0].HookError, wantHookErr) {
		t.Errorf("started result hook error = %v, want %v", results[0].HookError, wantHookErr)
	}
	if results[0].Output == "tool execution canceled" {
		t.Errorf("started result was replaced with a synthetic cancellation: %+v", results[0])
	}
	select {
	case <-laterStarted:
		t.Error("later execution stage started after cancellation")
	default:
	}
}

func TestExecuteBatchDrainsAllStartedReadsAfterCancellation(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
	})
	manager.SetHookDispatcher(dispatcher)

	started := make(chan string, 2)
	cancellationObserved := make(chan string, 2)
	releaseReads := make(chan struct{})
	for _, name := range []string{"read-a", "read-b"} {
		manager.RegisterTool(scheduledTool{name: name, access: ToolAccessRead, execute: func(ctx context.Context) ToolResult {
			started <- name
			<-ctx.Done()
			cancellationObserved <- name
			<-releaseReads
			return ToolResult{Output: name + " stopped after cancellation", IsError: true}
		}})
	}
	laterStarted := make(chan struct{}, 1)
	manager.RegisterTool(scheduledTool{name: "later-write", access: ToolAccessWrite, execute: func(context.Context) ToolResult {
		laterStarted <- struct{}{}
		return ToolResult{Output: "later tool ran"}
	}})

	wantHookErr := errors.New("read post hook audit failed")
	postFinished := make(chan struct{}, 2)
	if err := dispatcher.Register(hook.EventPostToolUse, func(input hook.Input) error {
		if input.ToolName == "later-write" {
			return nil
		}
		postFinished <- struct{}{}
		return wantHookErr
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan []ToolResult, 1)
	go func() {
		done <- manager.ExecuteBatch(ctx, []Invocation{
			{ID: "read-a", Name: "read-a"},
			{ID: "read-b", Name: "read-b"},
			{ID: "later", Name: "later-write"},
		})
	}()

	waitForStarts(t, started, 2)
	cancel()
	waitForStarts(t, cancellationObserved, 2)

	var results []ToolResult
	returnedBeforeReadsFinished := false
	select {
	case results = <-done:
		returnedBeforeReadsFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseReads)
	waitForSignals(t, postFinished, 2)
	if results == nil {
		select {
		case results = <-done:
		case <-time.After(time.Second):
			t.Fatal("batch did not finish after all started reads and post hooks completed")
		}
	}

	if returnedBeforeReadsFinished {
		t.Errorf("ExecuteBatch returned before all started read post hooks completed: %+v", results)
	}
	if len(results) != 3 {
		t.Fatalf("results = %+v, want three entries", results)
	}
	for index := range 2 {
		if !errors.Is(results[index].HookError, wantHookErr) {
			t.Errorf("read result %d hook error = %v, want %v", index, results[index].HookError, wantHookErr)
		}
		if results[index].Output == "tool execution canceled" {
			t.Errorf("read result %d was replaced with a synthetic cancellation: %+v", index, results[index])
		}
	}
	select {
	case <-laterStarted:
		t.Error("later write stage started after cancellation")
	default:
	}
}

func TestUnknownToolUsesExclusiveAccessAndKeepsItsPosition(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	manager.RegisterTool(scheduledTool{name: "read", access: ToolAccessRead, execute: func(context.Context) ToolResult {
		return ToolResult{Output: "read"}
	}})

	if access := manager.ToolAccess("missing"); access != ToolAccessExclusive {
		t.Fatalf("unknown access = %q, want %q", access, ToolAccessExclusive)
	}
	results := manager.ExecuteBatch(context.Background(), []Invocation{{ID: "1", Name: "read"}, {ID: "2", Name: "missing"}, {ID: "3", Name: "read"}})
	if len(results) != 3 || !results[1].IsError || results[0].Output != "read" || results[2].Output != "read" {
		t.Fatalf("results = %+v", results)
	}
}

func waitForStarts(t *testing.T, started <-chan string, count int) {
	t.Helper()
	for range count {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for concurrent reads")
		}
	}
}

func waitForSignals(t *testing.T, signals <-chan struct{}, count int) {
	t.Helper()
	for range count {
		select {
		case <-signals:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for execution stage")
		}
	}
}

func assertNotSignaled(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
		t.Fatal("later execution stage started before prior stage completed")
	case <-time.After(20 * time.Millisecond):
	}
}
