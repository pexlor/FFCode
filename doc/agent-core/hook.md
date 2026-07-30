# Hook 钩子系统设计

## 系统设计目标

Hook 为 Agent 运行时提供可编程生命周期边界，同时保证 Hook 自身不会让主流程无限等待、无限输出或递归调用。

当前支持以下事件：

| Event | 触发位置 |
| --- | --- |
| `pre_tool_use` | 工具参数补齐之后、权限校验和执行之前 |
| `post_tool_use` | 工具成功、失败或权限拒绝产生最终结果之后 |
| `session_start` | 新建或恢复 Session；Conversation Service 与 Agent 对同一次启动合并执行 |
| `user_prompt_submit` | 用户 Prompt 持久化之前 |
| `stop` | 每个 Agent Turn 产生最终 `TurnEndEvent` 之前 |
| `pre_compact` | 确认有内容需要压缩、调用摘要器之前 |
| `post_compact` | 新摘要成功提交之后 |
| `subagent_start` | `Agent.RunSubagent` 调用工作函数之前 |
| `subagent_stop` | `Agent.RunSubagent` 工作函数退出之后，包括错误、取消和 panic |

## 架构设计

`internal/hook` 是不依赖 Agent、Tool、Conversation 或 Context 的中立包。`Dispatcher` 保存 Handler 的不可变快照并按注册顺序执行；启用后，`internal/app` 从工作区加载一个共享 Dispatcher，注入会话服务、Agent、工具管理器和上下文压缩器。

工具 Hook 位于 `ToolsManager.ExecuteInvocation`，因此直接调用和批量调度具有相同语义。Pre Hook 修改的参数会重新进入权限校验，不能借由 Hook 绕过授权。连续只读工具仍可并发，不同调用的 Hook 上下文彼此隔离。批量取消时，已进入工具或 Post Hook 边界的调用会先排空结果；仍停留在未响应 Context 的权限检查中的调用会被放弃，不再启动后续阶段。

`post_tool_use` 发生时工具可能已经产生副作用。Fail-closed 的 Post Hook 失败会通过 `ToolResult.HookError` 终止当前 Agent Turn，但保留工具原始 `IsError`，避免把已经成功的写操作伪装成可重试的工具失败。

Session 和 Prompt Hook 位于 Conversation Service。每次 New/Resume 都使用仅驻留内存的转换键：Service 和 Agent 对同一次转换合并执行，但之后再次 Resume 同一 Session 仍会触发新的 `session_start`。被拒绝的 Prompt 不会创建 Session、更新标题或进入 History；改写后的 Prompt 会同时用于持久化和模型输入。

Agent 使用统一的结束函数触发 `stop`，覆盖成功、Provider 失败、预算耗尽、取消、超时和无进展终止。取消后的 Stop Hook 使用 `context.WithoutCancel` 保留 Session 元数据，并由 Hook 自身超时继续约束。

## 详细设计

### 工作区配置

项目 Hook 默认关闭，因为配置中的命令会以 FFCode 进程的用户权限运行。确认工作区可信后，在用户配置 `~/.ffcode/config.yaml` 中显式启用：

```yaml
hooks:
  enabled: true
```

启用后读取第一个存在的配置：

1. `.agent/hooks.yaml`
2. `.agent/hooks.yml`
3. `.ffcode/hooks.yaml`

也可以用 `MYCODE_HOOK_CONFIG` 显式指定配置文件；该环境变量本身视为本次运行的启用授权。Hook 配置示例：

```yaml
timeout: 5s
max_output_bytes: 65536
max_depth: 8
max_invocations: 64
failure_policy: fail_open

policies:
  pre_tool_use: fail_closed

hooks:
  pre_tool_use:
    - command: ./scripts/check-tool.sh
      timeout: 2s
      max_output_bytes: 8192
  post_tool_use: ./scripts/audit-tool.sh
  stop:
    - command: ./scripts/notify.sh
      args: [--source, mycode]
```

标量命令通过 `/bin/sh -c` 执行；带 `args` 的映射默认直接执行指定程序。命令从 stdin 接收一行 JSON，并可读取：

- `MYCODE_HOOK_EVENT`
- `MYCODE_HOOK_DEPTH`
- `MYCODE_HOOK_SESSION_ID`
- `MYCODE_HOOK_TOOL_NAME`
- `MYCODE_HOOK_TOOL_USE_ID`
- `MYCODE_WORKSPACE`

项目 Hook 是受信任的本地代码，拥有当前 FFCode 进程的用户权限，不经过 Tool 权限网关。不要在不可信工作区启用 Hook 配置。

### 输入与输出

输入统一使用 `hook.Input`，包含 Event、Session、Workspace、Tool、Prompt、Result、Reason 和 Metadata。命令可以输出 JSON：

```json
{
  "decision": "allow",
  "reason": "checked",
  "additional_context": "optional context",
  "updated_input": {
    "arguments": {"path": "normalized/path"},
    "prompt": "rewritten prompt"
  }
}
```

`decision=deny|block|ask` 会阻止尚未发生的操作。显式拒绝属于业务决策，返回 `Result.Blocked=true`，不计为 Handler 失败。非 JSON stdout 作为普通 Hook 输出返回。

命令输出为 JSON 对象时使用严格字段校验；以 `{` 开头但语法损坏的 JSON、未知字段、错误类型或未知 `decision` 都按 Handler 失败处理，并交给事件的失败策略决定是否继续。

### 运行边界

- 每个 Handler 有独立超时，默认 5 秒。POSIX 平台上的外部命令超时会终止整个进程组；其他平台至少终止直接子进程。
- stdout、stderr、内嵌 Handler 字符串、失败诊断和 UpdatedInput 共享严格的 Dispatch 字节预算，默认 64 KiB；多个 Handler、规范化输入别名和 Tool 适配层诊断前缀也不能绕过该上限。
- 截断结果保持合法 UTF-8，并返回 `ErrOutputLimit`。实际阻断行为由失败策略决定。
- `fail_open` 记录失败后继续；`fail_closed` 和 `abort` 返回错误并阻止尚未发生的动作。
- 默认仅 `pre_tool_use` 使用 `fail_closed`，其他事件使用 `fail_open`。全局或逐事件配置可以覆盖默认值。
- Dispatcher 使用 Context 记录活动 Event、深度和总调用次数。同一 Event 重入、超过 `max_depth` 或 `max_invocations` 都返回 `ErrRecursionLimit`。
- 外部命令通过 `MYCODE_HOOK_DEPTH` 继承递归深度，避免 Hook 再启动 FFCode 时绕过保护。
- `DispatchOnce` 对并发调用进行单次合并，用于 Session 转换和 Prompt 去重；完成结果缓存有固定上限，避免长会话无限增长。

## 功能测试

覆盖范围包括：

- Handler 顺序、参数改写和显式拒绝；
- 工具成功、失败、权限拒绝、批量并发以及取消排空；
- Session 单次启动、Prompt 改写与拒绝不落盘；
- Stop 在成功和取消路径恰好执行一次；
- Compact 和 Subagent 的成对生命周期；
- 超时、进程组终止、损坏 JSON、严格输出上限和 UTF-8 截断；
- fail-open、fail-closed、同 Event 递归和并发 `DispatchOnce`；
- `go test -race` 下的 Dispatcher、Tool、Conversation、Context 和 Agent 路径。
