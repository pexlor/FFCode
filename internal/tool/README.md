# tool

本目录定义统一的 Tool 协议、注册表和授权执行器。

## 主要文件

- `tool.go`：Tool、Schema 和 Result 类型。
- `registry.go`：工具注册、查询和 Schema 发布。
- `executor.go`：Workspace 默认参数、权限校验和实际执行。
- `mcp.go`：将远端 MCP 工具适配为 `Tool`。
- `builtin/`：FFCode 自带工具实现。

默认工具集合、权限策略文件和 MCP 配置由 `internal/app` 组装，本目录不决定应用启动配置。
