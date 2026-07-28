package builtin

import "MyCode/internal/tool"

type ToolResult = tool.ToolResult
type ToolSchema = tool.ToolSchema

const (
	ToolAccessRead      = tool.ToolAccessRead
	ToolAccessWrite     = tool.ToolAccessWrite
	ToolAccessExclusive = tool.ToolAccessExclusive
)
