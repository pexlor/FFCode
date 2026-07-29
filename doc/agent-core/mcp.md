# MCP 设计

## 系统设计目标

MCP 模块通过 stdio 连接项目声明的外部 MCP Server，将远端工具适配成 FFCode 的统一 Tool。Agent 不感知工具来自内置实现还是 MCP，所有 MCP 调用仍经过统一调度、Hook 和权限检查。

当前范围只包含 MCP Client 的初始化、工具发现和工具调用，不实现资源、Prompt、采样或 Server 能力。

## 架构设计

```text
<workspace>/.agent/mcp.yaml
       |
       v
internal/mcp.Client -- stdio/JSON-RPC --> MCP Server process
       |
       v
tool.MCPTool -> ToolsManager -> Agent
```

`internal/app` 在启动时加载配置、启动所有 Server、发现工具并注册适配器；应用退出时关闭 stdin 并终止子进程。

## 详细设计

### 配置文件

配置路径固定为 `<workspace>/.agent/mcp.yaml`：

```yaml
mcpServers:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
    env:
      LOG_LEVEL: warn
```

`mcpServers` 至少包含一项，Server 名称和 `command` 不能为空。未找到该文件时跳过 MCP；文件存在但无效、Server 启动失败或工具发现失败时，应用初始化失败并关闭已经启动的 Server。

`env` 会追加到当前进程环境。配置中的命令拥有 FFCode 进程的操作系统权限，应只在可信 Workspace 中使用。

### 协议和握手

Client 启动子进程后使用一行一个 JSON 对象的 JSON-RPC 2.0 通信，当前声明 MCP 协议版本 `2025-06-18`：

1. 发送 `initialize`，包含空 capabilities 和 Client 信息；
2. 校验 Server 返回相同协议版本；
3. 发送 `notifications/initialized`；
4. 调用 `tools/list` 获取工具；
5. 运行期使用 `tools/call`。

请求 ID 原子递增，pending map 将并发响应路由到对应调用。写入使用互斥锁保证 JSON 帧不交错；Server 退出、读取失败或 Client 关闭时，所有 pending 请求都会收到错误。

### 工具命名和适配

远端工具注册名为：

```text
mcp_<server-name>_<remote-tool-name>
```

名称中的非字母、数字、下划线和连字符会替换为下划线。远端 Input Schema 原样传给模型；缺失时使用空 object Schema。文本 Content 直接拼接，其他 Content 类型编码为 JSON 文本。

MCP Tool 当前没有声明显式 Access，因此按 `exclusive` 调度。注册名与已有工具碰撞时启动失败，不允许静默覆盖。

### 取消和清理

调用 Context 取消时，只移除本地 pending 请求；当前实现没有发送 MCP cancellation notification。应用关闭时关闭 stdin、失败所有等待请求并终止 Server 进程。

## 功能测试

当前 MCP 的核心行为主要通过 Tool 适配和应用装配间接覆盖。新增协议能力时应补充基于本地伪 stdio Server 的测试，至少覆盖握手、并发响应、错误响应、取消、进程退出、工具命名和清理。

```bash
go test ./internal/mcp/... ./internal/tool/... ./internal/app/...
```
