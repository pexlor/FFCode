# 只读委派型 Subagent 实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为主 Agent 增加显式 `delegate_task` 工具，使其能在父预算和只读权限边界内并发运行独立子 Agent，并获得结构化、有来源的结果。

**架构：** `internal/subagent.Manager` 创建独立内存 Session 和只读 ToolManager，并复用现有 `agent.Agent` 循环。父 Agent 通过 context 提供事件接收器、每轮调用计数和并发安全的子预算预留能力；应用装配层注册委派工具。

**技术栈：** Go 1.25、现有 Agent/Tool/Permission/Hook 包、标准库 JSON/context/sync。

---

## 文件结构

- 创建 `internal/agent/child_runtime.go`：父运行的子预算预留、调用计数和事件接收 context。
- 修改 `internal/agent/run_budget.go`：让预算状态支持并发读取、记账和预留。
- 修改 `internal/agent/agent.go`：在工具执行上下文注入子运行能力。
- 修改 `internal/agent/events.go`：定义 Subagent 生命周期和包装事件。
- 创建 `internal/subagent/result.go`：请求、状态、证据和稳定 JSON 结果。
- 创建 `internal/subagent/readonly.go`：构造硬限制的只读工具注册表和权限策略。
- 创建 `internal/subagent/manager.go`：生命周期、并发、子 Session、事件聚合和预算结算。
- 创建 `internal/subagent/tool.go`：实现 `delegate_task` schema、参数校验和结果序列化。
- 修改 `internal/app/bootstrap.go`、`internal/app/tools.go`：注册 Manager 和默认权限。
- 修改 `internal/protocol/encoder.go`、`internal/ui/terminal/repl.go`：兼容新增事件。

### 任务 1：父运行资源能力

- [x] 编写 `internal/agent/child_runtime_test.go`，验证多个并发预留不会超过父剩余 token/tool 预算，提交的实际用量进入父快照，调用次数限制逐轮独立。
- [x] 运行 `go test ./internal/agent -run 'TestChildBudget|TestClaimSubagent'`，确认因 API 缺失而失败。
- [x] 在 `child_runtime.go` 定义 `ReserveChildBudget`、`ChildBudgetReservation`、`ClaimSubagentCall`、事件 sink；为 `runBudgetState` 增加互斥保护。
- [x] 在 `agent.go` 的每轮 context 注入预算账户和调用计数，并在执行工具批次前注入事件 sink。
- [x] 运行目标测试与 `go test ./internal/agent`，确认通过。

### 任务 2：只读 Manager

- [x] 编写 `internal/subagent/readonly_test.go`，断言只出现 `ReadFile`、`Grep`、`Glob`，写工具、Bash 和 `delegate_task` 均不可执行。
- [x] 编写 `internal/subagent/manager_test.go`，使用脚本化 LLM 验证独立 Session、结构化证据、预算结算、父取消、并发上限、结果事件身份和禁止嵌套。
- [x] 运行 `go test ./internal/subagent`，确认包/API 缺失导致失败。
- [x] 实现 `result.go` 和 `readonly.go`，只读权限策略固定允许三个读取工具并锁定 workspace。
- [x] 实现 `manager.go`，默认单子任务预算为 2 分钟、30 次工具调用、4,000 输出 token；每轮最多 8 次、全局最多并发 4 次。
- [x] 运行 `go test ./internal/subagent`，确认通过。

### 任务 3：delegate_task 工具与装配

- [x] 编写 `internal/subagent/tool_test.go`，验证 schema、必填 task、负预算拒绝、稳定 JSON 和失败结果保持为可供主模型消费的工具输出。
- [x] 运行 `go test ./internal/subagent -run TestDelegateTask`，确认失败。
- [x] 实现 `tool.go`，解析 `task/context/max_duration_ms/max_input_tokens/max_output_tokens/max_tool_calls` 并调用 Manager。
- [x] 修改应用装配，向主 ToolManager 注册 `delegate_task`，默认权限策略允许它；子 ToolManager 不注册它。
- [x] 添加 `internal/app` 装配测试并运行 `go test ./internal/subagent ./internal/app`。

### 任务 4：事件协议和终端兼容

- [x] 先在 `internal/protocol/encoder_test.go` 和 `internal/ui/terminal/repl_test.go` 添加 Subagent start/wrapped/stop 事件测试。
- [x] 运行 `go test ./internal/protocol ./internal/ui/terminal`，确认 unsupported event 失败。
- [x] 实现 JSONL `subagent_started/subagent_event/subagent_finished` 编码；终端只渲染紧凑 start/stop 状态并忽略内部噪声。
- [x] 运行协议和终端测试，确认通过。

### 任务 5：全量验证

- [x] 对所有变更 Go 文件运行 `gofmt`。
- [x] 运行 `go test ./...`。
- [x] 运行 `go test -race ./internal/subagent ./internal/agent ./internal/tool`。
- [x] 对照设计验收标准和 `git diff --check`，确认无范围遗漏、格式错误或用户在途改动被覆盖。
