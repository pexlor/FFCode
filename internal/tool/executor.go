package tool

import (
	"FFCode/internal/hook"
	"FFCode/internal/permission"
	"context"
	"fmt"
	"os"
	"strings"
)

type ToolsManager struct {
	registry    *Registry
	permissions permission.PermissionManager
	workspace   string
	hooks       *hook.Dispatcher
}

func NewToolsManager() *ToolsManager {
	workspace, err := os.Getwd()
	if err != nil {
		return &ToolsManager{registry: NewRegistry()}
	}
	m, err := NewToolsManagerForWorkspace(workspace)
	if err != nil {
		return &ToolsManager{registry: NewRegistry()}
	}
	return m
}

func NewToolsManagerForWorkspace(workspace string) (*ToolsManager, error) {
	manager, err := permission.NewManager(permission.DefaultPolicy(workspace))
	if err != nil {
		return nil, err
	}
	return &ToolsManager{registry: NewRegistry(), permissions: manager, workspace: workspace}, nil
}

func (m *ToolsManager) RegisterTool(t Tool) {
	m.registry.Register(t)
}

func (m *ToolsManager) GetTool(name string) Tool {
	return m.registry.Get(name)
}

// SetPermissionManager replaces the mandatory permission gateway used by Execute.
// Passing nil restores default-deny behavior rather than disabling authorization.
func (m *ToolsManager) SetPermissionManager(manager permission.PermissionManager) {
	m.permissions = manager
}

func (m *ToolsManager) PermissionManager() permission.PermissionManager { return m.permissions }

// SetHookDispatcher installs the shared lifecycle hook dispatcher. Hook
// execution remains inside ToolsManager so direct calls and scheduled batches
// observe identical pre/post behavior.
func (m *ToolsManager) SetHookDispatcher(dispatcher *hook.Dispatcher) { m.hooks = dispatcher }

func (m *ToolsManager) HookDispatcher() *hook.Dispatcher { return m.hooks }

// Execute is the only tool execution entry point used by the agent runtime.
func (m *ToolsManager) Execute(ctx context.Context, name string, args map[string]any) ToolResult {
	return m.ExecuteInvocation(ctx, Invocation{Name: name, Arguments: args})
}

// ExecuteInvocation executes one identified invocation through hooks,
// authorization, and the registered tool.
func (m *ToolsManager) ExecuteInvocation(ctx context.Context, invocation Invocation) ToolResult {
	return m.executeInvocationWithCommit(ctx, invocation, nil)
}

func (m *ToolsManager) executeInvocationWithCommit(ctx context.Context, invocation Invocation, commit func() bool) ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	name := invocation.Name
	args := cloneArguments(invocation.Arguments)
	if args == nil {
		args = make(map[string]any)
	}
	m.applyWorkspaceDefaults(name, args)
	base, _ := hook.InputFromContext(ctx)
	base.Event = hook.EventPreToolUse
	base.Workspace = firstNonEmpty(base.Workspace, m.workspace)
	base.ToolName = name
	base.ToolUseID = invocation.ID
	base.Arguments = cloneArguments(args)
	hookContext := hook.WithInput(ctx, base)
	if m.hooks != nil {
		outcome, err := m.hooks.Dispatch(hookContext, hook.EventPreToolUse, base)
		if err != nil {
			return ToolResult{Output: m.hooks.BoundDiagnostic("pre_tool_use hook failed: " + err.Error()), IsError: true}
		}
		if outcome.Blocked {
			reason := outcome.Reason
			if reason == "" {
				reason = "hook denied operation"
			}
			return ToolResult{Output: m.hooks.BoundDiagnostic("pre_tool_use hook blocked tool: " + reason), IsError: true}
		}
		args = updatedArguments(args, outcome.UpdatedInput)
		m.applyWorkspaceDefaults(name, args)
		base.Arguments = cloneArguments(args)
		hookContext = hook.WithInput(ctx, base)
	}
	result, toolStarted := m.executeAuthorized(hookContext, name, args, commit)
	if m.hooks == nil {
		return result
	}
	if !toolStarted {
		if ctx.Err() != nil {
			return result
		}
		if commit != nil && !commit() {
			return result
		}
	}
	postInput := base
	postInput.Event = hook.EventPostToolUse
	postInput.Arguments = cloneArguments(args)
	postInput.ToolOutput = result.Output
	postInput.Output = result.Output
	postInput.IsError = result.IsError
	postInput.ToolResult = &hook.ToolResult{Output: result.Output, IsError: result.IsError}
	postContext := hook.WithInput(context.WithoutCancel(ctx), postInput)
	postOutcome, err := m.hooks.Dispatch(postContext, hook.EventPostToolUse, postInput)
	if err != nil {
		// Preserve the tool's outcome: callers must not retry an already completed
		// side effect merely because its post hook failed.
		result.HookError = err
		result.Output = appendHookDiagnostic(result.Output, "post_tool_use hook failed: "+err.Error(), m.hooks)
		return result
	}
	if postOutcome.Blocked {
		result.HookError = fmt.Errorf("%w: %s", hook.ErrHookDenied, postOutcome.Reason)
		result.Output = appendHookDiagnostic(result.Output, "post_tool_use hook blocked result: "+postOutcome.Reason, m.hooks)
	}
	return result
}

func (m *ToolsManager) executeAuthorized(ctx context.Context, name string, args map[string]any, commit func() bool) (ToolResult, bool) {
	registered := m.GetTool(name)
	if registered == nil {
		return ToolResult{Output: fmt.Sprintf("tool %q is not registered", name), IsError: true}, false
	}
	if m.permissions == nil {
		return ToolResult{Output: "permission denied: permission manager is not configured", IsError: true}, false
	}
	if ctx.Err() != nil {
		return canceledToolResult(ctx), false
	}
	req := buildPermissionRequest(name, args)
	result, err := m.permissions.Authorize(ctx, req)
	if ctx.Err() != nil {
		return canceledToolResult(ctx), false
	}
	if err != nil {
		// An authorization error can still include a decision and reason (for
		// example, when an audit write fails after a policy denial). Keep that
		// information in the tool result so the next model request can explain
		// why the call was not run instead of retrying blindly.
		if result.Decision != "" || result.Reason != "" {
			return ToolResult{Output: fmt.Sprintf("permission %s: %s (permission check failed: %v)", result.Decision, result.Reason, err), IsError: true}, false
		}
		return ToolResult{Output: fmt.Sprintf("permission check failed: %v", err), IsError: true}, false
	}
	if result.Decision != permission.Allow {
		return ToolResult{Output: fmt.Sprintf("permission %s: %s", result.Decision, result.Reason), IsError: true}, false
	}
	if commit != nil && !commit() {
		return canceledToolResult(ctx), false
	}
	return registered.Execute(ctx, args), true
}

func cloneArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	result := make(map[string]any, len(arguments))
	for key, value := range arguments {
		result[key] = cloneArgumentValue(value)
	}
	return result
}

func cloneArgumentValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneArguments(typed)
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneArgumentValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func updatedArguments(current, updated map[string]any) map[string]any {
	if updated == nil {
		return current
	}
	for _, key := range []string{"arguments", "args", "tool_input"} {
		value, ok := updated[key]
		if !ok {
			continue
		}
		if arguments, ok := value.(map[string]any); ok {
			return cloneArguments(arguments)
		}
	}
	// Treat an UpdatedInput map without a wrapper as a direct argument patch.
	result := cloneArguments(current)
	for key, value := range updated {
		result[key] = value
	}
	return result
}

func appendHookDiagnostic(output, diagnostic string, dispatcher *hook.Dispatcher) string {
	if output == "" {
		return dispatcher.BoundDiagnostic(diagnostic)
	}
	if diagnostic == "" {
		return output
	}
	return output + dispatcher.BoundDiagnostic("\n"+diagnostic)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (m *ToolsManager) applyWorkspaceDefaults(name string, args map[string]any) {
	if m == nil || m.workspace == "" || args == nil {
		return
	}
	switch strings.ToLower(name) {
	case "bash":
		if value, ok := args["working_directory"].(string); !ok || strings.TrimSpace(value) == "" {
			args["working_directory"] = m.workspace
		}
	case "grep", "glob":
		if value, ok := args["path"].(string); !ok || strings.TrimSpace(value) == "" {
			args["path"] = m.workspace
		}
	}
}

func (m *ToolsManager) BuildAllSchemas() []*ToolSchema {
	return m.registry.Schemas()
}

// BuildSchemas 按请求顺序返回工具 schema。
// 该方法只控制模型可见工具，不改变注册表，也不会绕过执行阶段的权限检查。
func (m *ToolsManager) BuildSchemas(names []string) ([]*ToolSchema, error) {
	return m.registry.SelectSchemas(names)
}

func buildPermissionRequest(name string, args map[string]any) permission.PermissionRequest {
	request := permission.PermissionRequest{ToolName: name, Arguments: args}
	lowerName := strings.ToLower(name)
	switch {
	case lowerName == "load_skill":
		request.Action = "read"
		request.RiskLevel = permission.Safe
	case strings.Contains(lowerName, "read"), strings.Contains(lowerName, "grep"), strings.Contains(lowerName, "glob"):
		request.Action = "read"
		request.RiskLevel = permission.Safe
	case strings.Contains(lowerName, "delete"), strings.Contains(lowerName, "remove"):
		request.Action = "delete"
		request.RiskLevel = permission.High
	default:
		request.Action = "write"
		request.RiskLevel = permission.Low
	}
	for key, value := range args {
		lowerKey := strings.ToLower(key)
		if lowerKey == "command" || lowerKey == "cmd" {
			request.Command, _ = value.(string)
		}
		if lowerKey == "working_directory" || lowerKey == "cwd" {
			request.WorkingDirectory, _ = value.(string)
		}
		if strings.Contains(lowerKey, "path") || strings.Contains(lowerKey, "file") || strings.Contains(lowerKey, "directory") {
			switch paths := value.(type) {
			case string:
				request.ResolvedPaths = append(request.ResolvedPaths, paths)
			case []string:
				request.ResolvedPaths = append(request.ResolvedPaths, paths...)
			case []any:
				for _, path := range paths {
					if stringPath, ok := path.(string); ok {
						request.ResolvedPaths = append(request.ResolvedPaths, stringPath)
					}
				}
			}
		}
	}
	return request
}
