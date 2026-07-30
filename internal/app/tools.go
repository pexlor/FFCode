package app

import (
	"FFCode/internal/permission"
	"FFCode/internal/tool"
	"FFCode/internal/tool/builtin"
	"context"
	"fmt"
	"os"
	"path/filepath"
)

func createTools(ctx context.Context, workspace string) (*tool.ToolsManager, func(), error) {
	manager, err := tool.NewToolsManagerForWorkspace(workspace)
	if err != nil {
		return nil, nil, err
	}
	manager.RegisterTool(&builtin.ReadFileTool{})
	manager.RegisterTool(&builtin.WriteFileTool{})
	manager.RegisterTool(&builtin.EditFileTool{})
	manager.RegisterTool(&builtin.GrepTool{})
	manager.RegisterTool(&builtin.GlobTool{})
	manager.RegisterTool(builtin.NewBashTool())

	policy := permission.DefaultPolicy(workspace)
	policyPath := filepath.Join(workspace, ".agent", "permission.yaml")
	if _, statErr := os.Stat(policyPath); statErr == nil {
		loaded, loadErr := permission.LoadPolicy(policyPath)
		if loadErr == nil {
			policy = loaded
		} else {
			policy.Tools = make(map[string]permission.ToolPolicy)
		}
	} else if os.IsNotExist(statErr) {
		allowDefaultTools(&policy)
	} else {
		policy.Tools = make(map[string]permission.ToolPolicy)
	}
	confirmer := &permission.TerminalConfirmer{In: os.Stdin, Out: os.Stderr}
	permissionManager, err := permission.NewManager(policy, permission.WithConfirmer(confirmer))
	if err != nil {
		return nil, nil, err
	}
	manager.SetPermissionManager(permissionManager)

	configPath := filepath.Join(workspace, ".agent", "mcp.yaml")
	if _, statErr := os.Stat(configPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return manager, func() {}, nil
		}
		return nil, nil, statErr
	}
	mcpTools, closeAll, err := tool.LoadMCPTools(ctx, configPath)
	if err != nil {
		return nil, nil, err
	}
	for _, registered := range mcpTools {
		if manager.GetTool(registered.Name()) != nil {
			closeAll()
			return nil, nil, fmt.Errorf("MCP tool name collision: %q", registered.Name())
		}
		manager.RegisterTool(registered)
	}
	return manager, closeAll, nil
}

func allowDefaultTools(policy *permission.Policy) {
	policy.Tools["load_skill"] = permission.ToolPolicy{Permission: permission.Allow}
	policy.Tools["delegatetask"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{ReadOnly: true}}
	policy.Tools["readfile"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{ReadOnly: true}}
	policy.Tools["bash"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{CanWrite: true, CanDelete: true}}
	policy.Tools["writefile"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{CanWrite: true}}
	policy.Tools["editfile"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{CanWrite: true}}
	policy.Tools["grep"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{ReadOnly: true}}
	policy.Tools["glob"] = permission.ToolPolicy{Permission: permission.Allow, ToolPermission: permission.ToolPermission{ReadOnly: true}}
}
