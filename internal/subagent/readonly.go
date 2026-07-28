package subagent

import (
	"MyCode/internal/permission"
	"MyCode/internal/tool"
	"MyCode/internal/tool/builtin"
)

func newReadOnlyTools(workspace string) (*tool.ToolsManager, error) {
	manager, err := tool.NewToolsManagerForWorkspace(workspace)
	if err != nil {
		return nil, err
	}
	policy := permission.DefaultPolicy(workspace)
	for _, name := range []string{"readfile", "grep", "glob"} {
		policy.Tools[name] = permission.ToolPolicy{
			Permission:     permission.Allow,
			ToolPermission: permission.ToolPermission{ReadOnly: true},
		}
	}
	permissions, err := permission.NewManager(policy)
	if err != nil {
		return nil, err
	}
	manager.SetPermissionManager(permissions)
	manager.RegisterTool(&builtin.ReadFileTool{})
	manager.RegisterTool(&builtin.GrepTool{})
	manager.RegisterTool(&builtin.GlobTool{})
	return manager, nil
}
