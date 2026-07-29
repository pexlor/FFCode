# Agent Loop 系统设计

## 系统设计目标

Agent 负责把一次用户请求推进到可解释的终止状态。它协调上下文、模型和工具，但不负责加载配置、管理具体存储格式或渲染终端 UI。

主要目标：

- 模型响应、工具调用和结果形成闭环，并通过 UI 无关事件对外发布。
- 用时间、Token、工具调用和 Provider 重试预算限制单次 Run。
- 对临时 Provider 故障安全重试，不重复提交半截模型输出。
- 识别阶段、工作区变化、验证结果和无进展循环。
- 在取消、超时或崩溃边界保存检查点，恢复时不重放状态未知的工具。

## 架构设计

```text
conversation.Session
       |
       v
agent.Agent ----------------------> AgentEvent channel
  |        |          |                    |
  |        |          +-> CheckpointStore  +-> terminal/jsonl
  |        +-> ToolsManager
  +-> ContextManager -> LLMClient
```

`Agent` 依赖抽象能力并由 `internal/app` 注入。模型的一次流式 attempt 先在内存中缓冲；只有流正常结束，文本、Thinking 和 Tool Call 才整体写入 Session 并发布。这样 429、529、可恢复 5xx 或临时传输错误重试时不会产生重复输出或重复工具调用。

工具调用批次交给 `ToolsManager.ExecuteBatch`。相邻只读工具并发，写和独占工具形成屏障，结果始终按模型原始调用顺序返回。

## 详细设计

### 运行流程

1. 校验 Session 和 RunBudget，创建带硬超时的 Context。
2. 执行 `session_start`、`user_prompt_submit` 等适用 Hook，并恢复未完成检查点。
3. 记录 Git Workspace baseline，进入 `Explore`。
4. ContextManager 构建 `ContextView`，Agent 调用 LLM。
5. 模型若返回工具调用，提交完整 attempt，预留工具预算并执行批次。
6. 将工具调用和结果写回 Session，保存 `model` 或 `tools` 边界检查点，继续循环。
7. 模型产生最终回复、预算不足、取消、超时、无进展或错误时，执行质量门禁、`stop` Hook，并发出唯一的 `TurnEndEvent`。

### 预算与重试

默认 RunBudget：20 分钟、2,000,000 输入 Token、128,000 输出 Token、512 次工具调用、2 次 Provider 重试。显式传入预算时，字段为零表示该项不限制。

达到软预算时进入 `Finalize`，要求模型收尾；达到硬预算时以 `incomplete/budget_exhausted` 结束。可重试 Provider 错误采用有上限的指数退避，并发布 `ProviderRetryEvent`。

### 阶段与证据

阶段状态为：

```text
Explore -> Implement -> Verify -> Finalize
             ^           |
             +-----------+  验证后再次修改
```

- 相对本 Run baseline 出现新变化时进入 `Implement`。
- 修改后执行验证命令时进入 `Verify`，无论验证成功或失败。
- 模型结束或软预算触发时进入 `Finalize`。
- 启动前已有的 dirty 文件不会自动归因给当前 Run。

质量门禁发布 `QG001` 至 `QG008` 告警，覆盖未验证、验证失败、只改测试、修改测试期望、弱验证、验证后再改代码、Diff 证据不完整和大量工具调用后空补丁。告警不改变 Turn 状态或退出码。

### 无进展控制

Agent 为工具名、参数和结果生成指纹。重复调用先产生提醒，再拒绝完全相同的调用；连续多轮没有新证据时进入收尾或以 `no_progress` 结束。进展事件通过 `ProgressEvent` 暴露。

### 检查点与恢复

检查点保存在 `<workspace>/.context/checkpoints/<session-id>/`，边界包括 `model`、`tools`、`recovery`、`interrupted` 和 `completed`。文件 Store 使用原子发布并只保留最近两代。

恢复时保留已完成工具的历史，对未完成工具追加“执行状态未知且未重放”的错误结果；若 Workspace 指纹变化，会要求重新读取当前文件。此策略避免重复执行可能已有副作用的操作。

### 事件和终止状态

主要事件包括文本、Thinking、阶段转换、Provider 重试、工具调用与执行、工具结果、进展、质量告警、子 Agent 生命周期和 `TurnEndEvent`。

`TurnEndEvent` 状态为 `completed`、`incomplete`、`failed` 或 `cancelled`；停止原因包括 `end_turn`、`max_tokens`、`cancelled`、`deadline_exceeded`、`provider_error`、`budget_exhausted`、`no_progress` 和 `agent_error`。

### 子 Agent

`delegateTask` 是只读工具，由 `internal/subagent` 创建隔离子 Agent。子 Agent 具有独立会话、消息历史和预算，调用事件包装为父 Agent 的 `SubagentEvent`，生命周期同时经过 `subagent_start`/`subagent_stop` Hook。它不能调用写工具，也不能递归委派。

## 功能测试

`internal/agent` 测试覆盖正常循环、Provider 重试、预算、取消、阶段转换、证据和质量门禁、无进展检测、检查点恢复、Hook 及子运行时。`internal/subagent` 覆盖只读工具集、事件转发、取消与预算隔离。

验证命令：

```bash
go test ./internal/agent/... ./internal/subagent/...
go test -race ./internal/agent/... ./internal/subagent/...
```
