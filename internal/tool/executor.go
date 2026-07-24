package tool

import (
	"MyCode/internal/permission"
	"context"
	"fmt"
	"os"
	"strings"
)

type ToolsManager struct {
	registry    *Registry
	permissions permission.PermissionManager
	workspace   string
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

// Execute is the only tool execution entry point used by the agent runtime.
func (m *ToolsManager) Execute(ctx context.Context, name string, args map[string]any) ToolResult {
	registered := m.GetTool(name)
	if registered == nil {
		return ToolResult{Output: fmt.Sprintf("tool %q is not registered", name), IsError: true}
	}
	if m.permissions == nil {
		return ToolResult{Output: "permission denied: permission manager is not configured", IsError: true}
	}
	if args == nil {
		args = make(map[string]any)
	}
	m.applyWorkspaceDefaults(name, args)
	req := buildPermissionRequest(name, args)
	result, err := m.permissions.Authorize(ctx, req)
	if err != nil {
		// An authorization error can still include a decision and reason (for
		// example, when an audit write fails after a policy denial). Keep that
		// information in the tool result so the next model request can explain
		// why the call was not run instead of retrying blindly.
		if result.Decision != "" || result.Reason != "" {
			return ToolResult{Output: fmt.Sprintf("permission %s: %s (permission check failed: %v)", result.Decision, result.Reason, err), IsError: true}
		}
		return ToolResult{Output: fmt.Sprintf("permission check failed: %v", err), IsError: true}
	}
	if result.Decision != permission.Allow {
		return ToolResult{Output: fmt.Sprintf("permission %s: %s", result.Decision, result.Reason), IsError: true}
	}
	return registered.Execute(ctx, args)
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
