package tool

import (
	"context"
	"sync"
	"testing"
	"time"

	"MyCode/internal/permission"
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
