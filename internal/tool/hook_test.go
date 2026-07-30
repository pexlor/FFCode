package tool

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"FFCode/internal/hook"
)

type hookTestTool struct {
	execute func(context.Context, map[string]any) ToolResult
}

func (hookTestTool) Name() string        { return "hook-test" }
func (hookTestTool) Description() string { return "hook integration test" }
func (hookTestTool) Schema() *ToolSchema {
	return &ToolSchema{Name: "hook-test", Access: ToolAccessExclusive, Parameters: map[string]any{"type": "object"}}
}
func (t hookTestTool) Execute(ctx context.Context, arguments map[string]any) ToolResult {
	return t.execute(ctx, arguments)
}

func TestExecuteInvocationRunsPreAndPostHooksAroundTool(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.DefaultConfig())
	manager.SetHookDispatcher(dispatcher)

	var sequence []string
	if err := dispatcher.Register(hook.PreToolUse, func(input hook.Input) hook.Output {
		sequence = append(sequence, "pre:"+input.ToolUseID+":"+input.Arguments["value"].(string))
		return hook.Output{UpdatedInput: map[string]any{
			"arguments": map[string]any{"value": "updated"},
		}}
	}); err != nil {
		t.Fatal(err)
	}
	manager.RegisterTool(hookTestTool{execute: func(_ context.Context, arguments map[string]any) ToolResult {
		sequence = append(sequence, "tool:"+arguments["value"].(string))
		return ToolResult{Output: "tool output"}
	}})
	if err := dispatcher.Register(hook.PostToolUse, func(input hook.Input) hook.Output {
		sequence = append(sequence, "post:"+input.ToolResult.Output)
		if input.ToolUseID != "call-7" || input.ToolName != "hook-test" {
			t.Errorf("post input identity = %+v", input)
		}
		if input.ToolResult.IsError {
			t.Errorf("post input unexpectedly marks tool error: %+v", input.ToolResult)
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}

	arguments := map[string]any{"value": "original"}
	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-7", Name: "hook-test", Arguments: arguments})
	if result.IsError || result.Output != "tool output" {
		t.Fatalf("tool result = %+v", result)
	}
	if got, want := strings.Join(sequence, ","), "pre:call-7:original,tool:updated,post:tool output"; got != want {
		t.Fatalf("execution sequence = %q, want %q", got, want)
	}
	if arguments["value"] != "original" {
		t.Fatalf("ExecuteInvocation mutated caller arguments: %+v", arguments)
	}
}

func TestPreToolUseDenialSkipsToolAndPostHook(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.DefaultConfig())
	manager.SetHookDispatcher(dispatcher)

	var toolCalls atomic.Int32
	var postCalls atomic.Int32
	manager.RegisterTool(hookTestTool{execute: func(context.Context, map[string]any) ToolResult {
		toolCalls.Add(1)
		return ToolResult{Output: "unexpected"}
	}})
	if err := dispatcher.Register(hook.PreToolUse, func(hook.Input) hook.Output {
		return hook.Output{Decision: hook.DecisionDeny, Reason: "policy denied"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.PostToolUse, func(hook.Input) hook.Output {
		postCalls.Add(1)
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-denied", Name: "hook-test"})
	if !result.IsError || !strings.Contains(result.Output, "policy denied") {
		t.Fatalf("denied result = %+v", result)
	}
	if toolCalls.Load() != 0 || postCalls.Load() != 0 {
		t.Fatalf("tool calls = %d, post calls = %d; want both zero", toolCalls.Load(), postCalls.Load())
	}
}

func TestPostToolUseRunsForToolError(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.DefaultConfig())
	manager.SetHookDispatcher(dispatcher)
	manager.RegisterTool(hookTestTool{execute: func(context.Context, map[string]any) ToolResult {
		return ToolResult{Output: "tool failed", IsError: true}
	}})

	var observed hook.ToolResult
	if err := dispatcher.Register(hook.PostToolUse, func(input hook.Input) hook.Output {
		observed = *input.ToolResult
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-error", Name: "hook-test"})
	if !result.IsError || result.Output != "tool failed" {
		t.Fatalf("tool result = %+v", result)
	}
	if !observed.IsError || observed.Output != "tool failed" {
		t.Fatalf("post hook observed %+v", observed)
	}
}

func TestPostToolUseRunsAfterToolContextCancellation(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.DefaultConfig())
	manager.SetHookDispatcher(dispatcher)
	started := make(chan struct{})
	manager.RegisterTool(hookTestTool{execute: func(ctx context.Context, _ map[string]any) ToolResult {
		close(started)
		<-ctx.Done()
		return ToolResult{Output: "tool canceled", IsError: true}
	}})
	var postCalls atomic.Int32
	if err := dispatcher.Register(hook.PostToolUse, func(ctx context.Context, input hook.Input) hook.Output {
		postCalls.Add(1)
		if ctx.Err() != nil {
			t.Errorf("post hook context remained canceled: %v", ctx.Err())
		}
		if input.ToolResult == nil || input.ToolResult.Output != "tool canceled" {
			t.Errorf("post input = %+v", input)
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan ToolResult, 1)
	go func() {
		done <- manager.ExecuteInvocation(ctx, Invocation{ID: "call-canceled", Name: "hook-test"})
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(time.Second):
		t.Fatal("tool did not start")
	}
	select {
	case result := <-done:
		if !result.IsError || result.Output != "tool canceled" {
			t.Fatalf("tool result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("tool invocation did not finish")
	}
	if postCalls.Load() != 1 {
		t.Fatalf("post hook calls = %d, want 1", postCalls.Load())
	}
}

func TestPostToolFailurePreservesCompletedToolOutcome(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	dispatcher := hook.New(hook.Config{
		FailurePolicy: hook.FailureOpen,
		Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
	})
	manager.SetHookDispatcher(dispatcher)
	manager.RegisterTool(hookTestTool{execute: func(context.Context, map[string]any) ToolResult {
		return ToolResult{Output: "write completed"}
	}})
	wantErr := errors.New("audit unavailable")
	if err := dispatcher.Register(hook.PostToolUse, func(hook.Input) error { return wantErr }); err != nil {
		t.Fatal(err)
	}

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-written", Name: "hook-test"})
	if result.IsError || !errors.Is(result.HookError, wantErr) {
		t.Fatalf("tool result = %+v", result)
	}
	if !strings.Contains(result.Output, "write completed") || !strings.Contains(result.Output, "post_tool_use hook failed") {
		t.Fatalf("tool output = %q", result.Output)
	}
}

func TestHookFailureDiagnosticsRespectConfiguredOutputLimit(t *testing.T) {
	const limit = 32
	largeError := errors.New(strings.Repeat("错", 256))

	t.Run("pre hook", func(t *testing.T) {
		manager := NewToolsManager()
		dispatcher := hook.New(hook.Config{MaxOutputBytes: limit, FailurePolicy: hook.FailureClosed})
		manager.SetHookDispatcher(dispatcher)
		if err := dispatcher.Register(hook.PreToolUse, func(hook.Input) error { return largeError }); err != nil {
			t.Fatal(err)
		}

		result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-pre-limit", Name: "hook-test"})
		if !result.IsError || len(result.Output) > limit || !utf8.ValidString(result.Output) {
			t.Fatalf("pre hook diagnostic len=%d valid=%v result=%+v", len(result.Output), utf8.ValidString(result.Output), result)
		}
	})

	t.Run("post hook", func(t *testing.T) {
		manager := NewToolsManager()
		manager.SetPermissionManager(allowAllPermissions{})
		dispatcher := hook.New(hook.Config{
			MaxOutputBytes: limit,
			FailurePolicy:  hook.FailureOpen,
			Policies:       map[hook.Event]hook.FailurePolicy{hook.EventPostToolUse: hook.FailureClosed},
		})
		manager.SetHookDispatcher(dispatcher)
		manager.RegisterTool(hookTestTool{execute: func(context.Context, map[string]any) ToolResult {
			return ToolResult{}
		}})
		if err := dispatcher.Register(hook.PostToolUse, func(hook.Input) error { return largeError }); err != nil {
			t.Fatal(err)
		}

		result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-post-limit", Name: "hook-test"})
		if result.HookError == nil || len(result.Output) > limit || !utf8.ValidString(result.Output) {
			t.Fatalf("post hook diagnostic len=%d valid=%v result=%+v", len(result.Output), utf8.ValidString(result.Output), result)
		}
	})
}
