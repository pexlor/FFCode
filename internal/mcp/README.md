# mcp

本目录实现 Model Context Protocol（MCP）Client，用于连接外部 MCP Server。

## 主要职责

- 加载 `.agent/mcp.yaml` 中的 Server 配置。
- 启动和关闭 MCP 子进程。
- 完成初始化握手、工具发现和工具调用。
- 管理请求 ID、并发响应和协议错误。

MCP 工具会在应用装配阶段适配为统一的 `tool.Tool`，Agent 不感知工具来源。
