package tool

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"FFCode/internal/hook"
	"FFCode/internal/permission"
)

type recordingPermissionManager struct {
	mu      sync.Mutex
	request permission.PermissionRequest
}

type denyingPermissionManager struct{}

func (denyingPermissionManager) Authorize(context.Context, permission.PermissionRequest) (permission.PermissionResult, error) {
	return permission.PermissionResult{Decision: permission.Deny, Reason: "policy denied"}, nil
}

func (m *recordingPermissionManager) Authorize(_ context.Context, request permission.PermissionRequest) (permission.PermissionResult, error) {
	m.mu.Lock()
	m.request = request
	m.mu.Unlock()
	return permission.PermissionResult{Decision: permission.Allow}, nil
}

func TestExecuteInvocationRunsHooksAroundAuthorizedTool(t *testing.T) {
	manager := NewToolsManager()
	permissions := &recordingPermissionManager{}
	manager.SetPermissionManager(permissions)
	var executed map[string]any
	manager.RegisterTool(scheduledTool{name: "read", access: ToolAccessRead, execute: func(context.Context) ToolResult {
		return ToolResult{Output: "done"}
	}})
	// Replace the fixed scheduled tool with one that records arguments.
	manager.RegisterTool(argumentTool{name: "capture", execute: func(arguments map[string]any) ToolResult {
		executed = cloneArguments(arguments)
		return ToolResult{Output: "done"}
	}})

	dispatcher := hook.New(hook.DefaultConfig())
	if err := dispatcher.Register(hook.EventPreToolUse, func(_ context.Context, input hook.Input) (hook.Output, error) {
		if input.ToolUseID != "call-1" || input.ToolName != "capture" || input.Arguments["value"] != "before" {
			t.Fatalf("pre input = %+v", input)
		}
		return hook.Output{UpdatedInput: map[string]any{"arguments": map[string]any{"value": "after"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(hook.EventPostToolUse, func(_ context.Context, input hook.Input) (hook.Output, error) {
		if input.ToolUseID != "call-1" || input.ToolResult == nil || input.ToolResult.Output != "done" || input.ToolResult.IsError {
			t.Fatalf("post input = %+v", input)
		}
		return hook.Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetHookDispatcher(dispatcher)

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-1", Name: "capture", Arguments: map[string]any{"value": "before"}})
	if result.IsError || result.Output != "done" {
		t.Fatalf("tool result = %+v", result)
	}
	if executed["value"] != "after" {
		t.Fatalf("executed args = %#v", executed)
	}
	permissions.mu.Lock()
	defer permissions.mu.Unlock()
	if permissions.request.Arguments["value"] != "after" {
		t.Fatalf("authorized args = %#v", permissions.request.Arguments)
	}
}

func TestPreToolArgumentReplacementReappliesWorkspaceDefaults(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		arguments  map[string]any
		defaultKey string
	}{
		{name: "bash working directory", toolName: "bash", arguments: map[string]any{"command": "pwd"}, defaultKey: "working_directory"},
		{name: "grep path", toolName: "grep", arguments: map[string]any{"pattern": "needle"}, defaultKey: "path"},
		{name: "glob path", toolName: "glob", arguments: map[string]any{"pattern": "*.go"}, defaultKey: "path"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workspace := t.TempDir()
			manager, err := NewToolsManagerForWorkspace(workspace)
			if err != nil {
				t.Fatal(err)
			}
			permissions := &recordingPermissionManager{}
			manager.SetPermissionManager(permissions)
			var executed map[string]any
			manager.RegisterTool(argumentTool{name: test.toolName, execute: func(arguments map[string]any) ToolResult {
				executed = cloneArguments(arguments)
				return ToolResult{Output: "done"}
			}})

			dispatcher := hook.New(hook.DefaultConfig())
			if err := dispatcher.Register(hook.EventPreToolUse, func(hook.Input) hook.Output {
				return hook.Output{UpdatedInput: map[string]any{"arguments": cloneArguments(test.arguments)}}
			}); err != nil {
				t.Fatal(err)
			}
			manager.SetHookDispatcher(dispatcher)

			result := manager.ExecuteInvocation(context.Background(), Invocation{Name: test.toolName, Arguments: cloneArguments(test.arguments)})
			if result.IsError {
				t.Fatalf("tool result = %+v", result)
			}
			if got := executed[test.defaultKey]; got != workspace {
				t.Fatalf("executed %s = %#v, want %q", test.defaultKey, got, workspace)
			}
			permissions.mu.Lock()
			authorized := cloneArguments(permissions.request.Arguments)
			permissions.mu.Unlock()
			if got := authorized[test.defaultKey]; got != workspace {
				t.Fatalf("authorized %s = %#v, want %q", test.defaultKey, got, workspace)
			}
		})
	}
}

func TestPreToolHookCanDenyWithoutExecutingTool(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	var calls atomic.Int32
	manager.RegisterTool(argumentTool{name: "capture", execute: func(map[string]any) ToolResult {
		calls.Add(1)
		return ToolResult{Output: "unexpected"}
	}})
	dispatcher := hook.New(hook.DefaultConfig())
	if err := dispatcher.Register(hook.EventPreToolUse, func(hook.Input) hook.Output {
		return hook.Output{Decision: hook.DecisionDeny, Reason: "not allowed"}
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetHookDispatcher(dispatcher)

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-1", Name: "capture"})
	if !result.IsError || !strings.Contains(result.Output, "not allowed") || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
}

func TestPreToolHookFailureStrategyControlsExecution(t *testing.T) {
	for _, test := range []struct {
		name        string
		policy      hook.FailurePolicy
		wantCalls   int32
		wantFailure bool
	}{
		{name: "open", policy: hook.FailureOpen, wantCalls: 1},
		{name: "closed", policy: hook.FailureClosed, wantFailure: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager := NewToolsManager()
			manager.SetPermissionManager(allowAllPermissions{})
			var calls atomic.Int32
			manager.RegisterTool(argumentTool{name: "capture", execute: func(map[string]any) ToolResult {
				calls.Add(1)
				return ToolResult{Output: "done"}
			}})
			dispatcher := hook.New(hook.Config{
				FailurePolicy: test.policy,
				Policies:      map[hook.Event]hook.FailurePolicy{hook.EventPreToolUse: test.policy},
			})
			if err := dispatcher.Register(hook.EventPreToolUse, func(hook.Input) (hook.Output, error) {
				return hook.Output{}, errors.New("broken")
			}); err != nil {
				t.Fatal(err)
			}
			manager.SetHookDispatcher(dispatcher)

			result := manager.ExecuteInvocation(context.Background(), Invocation{Name: "capture"})
			if calls.Load() != test.wantCalls || result.IsError != test.wantFailure {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestPostToolHookObservesPermissionDenial(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(denyingPermissionManager{})
	var toolCalls atomic.Int32
	manager.RegisterTool(argumentTool{name: "capture", execute: func(map[string]any) ToolResult {
		toolCalls.Add(1)
		return ToolResult{Output: "unexpected"}
	}})
	dispatcher := hook.New(hook.DefaultConfig())
	var postCalls atomic.Int32
	if err := dispatcher.Register(hook.EventPostToolUse, func(input hook.Input) {
		postCalls.Add(1)
		if input.ToolResult == nil || !input.ToolResult.IsError || !strings.Contains(input.ToolResult.Output, "permission deny") {
			t.Errorf("post input = %+v", input)
		}
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetHookDispatcher(dispatcher)

	result := manager.ExecuteInvocation(context.Background(), Invocation{ID: "call-denied", Name: "capture"})
	if !result.IsError || toolCalls.Load() != 0 || postCalls.Load() != 1 {
		t.Fatalf("result=%+v tool calls=%d post calls=%d", result, toolCalls.Load(), postCalls.Load())
	}
}

func TestExecuteBatchPreservesInvocationIDsInHooks(t *testing.T) {
	manager := NewToolsManager()
	manager.SetPermissionManager(allowAllPermissions{})
	manager.RegisterTool(argumentTool{name: "capture", execute: func(map[string]any) ToolResult {
		return ToolResult{Output: "done"}
	}})
	dispatcher := hook.New(hook.DefaultConfig())
	var mu sync.Mutex
	seen := make(map[string]bool)
	if err := dispatcher.Register(hook.EventPreToolUse, func(input hook.Input) {
		mu.Lock()
		seen[input.ToolUseID] = true
		mu.Unlock()
	}); err != nil {
		t.Fatal(err)
	}
	manager.SetHookDispatcher(dispatcher)

	results := manager.ExecuteBatch(context.Background(), []Invocation{
		{ID: "call-a", Name: "capture"},
		{ID: "call-b", Name: "capture"},
	})
	mu.Lock()
	defer mu.Unlock()
	if len(results) != 2 || results[0].IsError || results[1].IsError || !seen["call-a"] || !seen["call-b"] {
		t.Fatalf("results=%+v seen=%v", results, seen)
	}
}

type argumentTool struct {
	name    string
	execute func(map[string]any) ToolResult
}

func (t argumentTool) Name() string        { return t.name }
func (t argumentTool) Description() string { return t.name }
func (t argumentTool) Schema() *ToolSchema {
	return &ToolSchema{Name: t.name, Access: ToolAccessRead, Parameters: map[string]any{"type": "object"}}
}
func (t argumentTool) Execute(_ context.Context, arguments map[string]any) ToolResult {
	return t.execute(arguments)
}
