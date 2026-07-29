# Tool 工具系统设计

## 系统设计目标

Tool 系统为内置工具、MCP 工具、Skill 和子 Agent 提供统一协议，并把 Workspace 默认值、Hook、权限检查、风险确认、调度和执行放在同一入口中。Agent 只提交调用批次，不直接决定并发策略或绕过授权。

## 架构设计

```text
Agent
  -> ToolsManager.ExecuteBatch
       -> Scheduler（read/write/exclusive）
       -> pre_tool_use Hook
       -> Workspace defaults
       -> PermissionManager.Authorize
       -> Tool.Execute
       -> post_tool_use Hook
```

`Registry` 以不区分大小写的名称保存 Tool，并向模型发布 Schema。`ToolsManager` 组合 Registry、PermissionManager 和 Hook Dispatcher。

## 详细设计

### Tool 协议

每个 Tool 实现 `Name`、`Description`、`Schema` 和 `Execute`。Schema 使用 JSON Schema 参数并声明调度访问级别：

- `read`：连续只读调用可并发；
- `write`：与前后调用形成屏障并串行执行；
- `exclusive`：默认最保守类别，未知工具也按此处理。

结果包含文本、`IsError` 和独立的 `HookError`。Post Hook 失败不能把已经成功且可能产生副作用的写操作伪装成工具失败。

### 内置工具

| 工具 | 调度类别 | 用途 |
| --- | --- | --- |
| `ReadFile` | read | 读取 Workspace 内文件 |
| `Grep` | read | 搜索文件内容 |
| `Glob` | read | 按模式发现文件 |
| `WriteFile` | write | 写入完整文件 |
| `EditFile` | write | 精确替换文件内容 |
| `Bash` | exclusive | 执行命令，默认按可能修改 Workspace 处理 |
| `load_skill` | write | 激活 Inline Skill，改变后续上下文和工具可见性 |
| `delegateTask` | read | 运行隔离的只读子 Agent |

MCP 工具在运行时适配为相同接口。

### 调度与取消

Scheduler 并发执行相邻只读段，在每个写或独占调用前后等待屏障。结果数组保持模型原始调用顺序。取消时，尚未跨过执行提交边界的调用不再启动；已开始执行的调用会排空真实结果和 Post Hook，避免副作用发生后被误报为“未执行”。

### Workspace 和参数

`ToolsManager` 在 Hook 和权限校验前补充需要的 Workspace 参数。Pre Hook 若改写参数，系统会重新应用 Workspace 默认值并重新授权。文件路径解析、符号链接和越界规则由权限系统统一处理。

### 权限配置

项目策略路径是 `<workspace>/.agent/permission.yaml`。不存在时，应用为默认内置工具注册策略；存在但加载失败时不会退回宽松策略。授权结果可以是允许、拒绝或要求终端确认。

```yaml
default: deny
workspace:
  root: .
tools:
  readfile:
    permission: allow
  bash:
    permission: confirm
    can_write: true
    can_delete: false
protected_paths:
  - ~/.ssh
```

Skill 的 `allowed-tools` 只缩小模型看到的工具集合，不授予权限。所有调用在执行时仍经过 PermissionManager。

### Hook 顺序

`pre_tool_use` 位于参数补齐之后、授权之前，可以改写或拒绝调用。`post_tool_use` 观察成功、工具失败和权限拒绝的最终结果。详细限制见 [hook.md](./hook.md)。

## 功能测试

测试覆盖注册和 Schema 选择、Workspace 参数、授权拒绝、Hook 参数改写、相邻读并发、写屏障、顺序保持、取消排空和竞态安全。

```bash
go test ./internal/tool/... ./internal/permission/...
go test -race ./internal/tool/...
```
