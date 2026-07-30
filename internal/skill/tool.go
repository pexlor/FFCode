package skill

import (
	"FFCode/internal/tool"
	"context"
	"fmt"
	"strings"
)

const LoadToolName = "load_skill"

type LoadTool struct{ Manager *Manager }

func (t *LoadTool) Name() string { return LoadToolName }
func (t *LoadTool) Description() string {
	return "Load and activate a named local skill SOP for the current task."
}
func (t *LoadTool) Schema() *tool.ToolSchema {
	return &tool.ToolSchema{Name: t.Name(), Description: t.Description(), Access: tool.ToolAccessWrite, Parameters: map[string]any{"type": "object", "properties": map[string]any{"name": map[string]any{"type": "string", "description": "The skill name from the available skills catalog."}, "arguments": map[string]any{"type": "string", "description": "Optional arguments passed to the skill."}}, "required": []string{"name"}}}
}
func (t *LoadTool) Execute(_ context.Context, args map[string]any) tool.ToolResult {
	name, _ := args["name"].(string)
	arguments, _ := args["arguments"].(string)
	if strings.TrimSpace(name) == "" {
		return tool.ToolResult{Output: "skill name is required", IsError: true}
	}
	active, err := t.Manager.Load(name, arguments)
	if err != nil {
		return tool.ToolResult{Output: err.Error(), IsError: true}
	}
	return tool.ToolResult{Output: fmt.Sprintf("skill %q activated (mode: %s, allowed tools: %s). Its SOP is active for subsequent requests.", active.Definition.Name, active.Definition.Mode, strings.Join(active.Definition.AllowedTools, ", "))}
}
