# 结构化 Agent 事件协议实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为 MyCode 增加稳定的 Agent 生命周期语义和 `--output-format jsonl` 机器接口，并迁移现有自动化调用方停止解析终端正文。

**架构：** `internal/agent` 是 Turn 终止状态的唯一所有者；`internal/protocol` 只负责把 AgentEvent 编码成版本化 JSONL；`internal/ui/jsonl` 负责按行读取请求和驱动会话；`internal/app` 选择 text 或 jsonl 前端。终端 UI 和自动化调用方消费同一套 Agent 事件，不再推断完成状态。

**技术栈：** Go、标准库 `encoding/json`、现有 AgentEvent/Conversation Service、Python `unittest`。

---

## 文件结构

- 修改 `internal/agent/events.go`：定义稳定的 Turn 状态、停止原因和 `TurnEndEvent`。
- 修改 `internal/agent/agent.go`：让所有退出路径产生且只产生一个 `TurnEndEvent`。
- 修改 `internal/agent/agent_retry_test.go`：适配终止事件并保留已有畸形工具输入重试测试。
- 创建 `internal/agent/agent_lifecycle_test.go`：覆盖完成、截断、取消、deadline 和错误终止。
- 创建 `internal/protocol/event.go`：定义版本 1 JSON envelope 和类型化 payload。
- 创建 `internal/protocol/encoder.go`：把 AgentEvent 编码成单行 JSON。
- 创建 `internal/protocol/encoder_test.go`：覆盖全部事件映射、序列号和无 ANSI 输出。
- 创建 `internal/ui/jsonl/run.go`：实现按行请求和 JSONL 事件输出。
- 创建 `internal/ui/jsonl/run_test.go`：使用可注入 TurnRunner 验证多 Turn、失败和异常关闭。
- 修改 `internal/ui/terminal/repl.go`：渲染 `TurnEndEvent`，不再根据 `ErrorEvent` 推断结束。
- 修改 `internal/ui/terminal/repl_test.go`：覆盖完成与失败终止渲染。
- 修改 `internal/app/options.go`：解析 `--output-format text|jsonl`。
- 修改 `internal/app/options_test.go`：覆盖默认值、合法值和非法值。
- 修改 `internal/app/app.go`：根据输出格式选择终端或 JSONL 运行器。
- 修改 `internal/app/README.md`、`cmd/FFCode/README.md`：记录机器接口。
- 修改 `benchmark/swebench-lite-20260726/run.py`：使用 JSONL 的 `turn_finished` 判定状态。
- 创建 `benchmark/swebench-lite-20260726/test_run.py`：覆盖正文不触发结束及停止状态分类。

### 任务 1：统一 Agent Turn 生命周期

**文件：**
- 修改：`internal/agent/events.go`
- 修改：`internal/agent/agent.go`
- 修改：`internal/agent/agent_retry_test.go`
- 创建：`internal/agent/agent_lifecycle_test.go`

- [ ] **步骤 1：编写停止原因映射的失败测试**

使用真实的脚本化 LLMClient 分别返回 `end_turn`、`stop`、`max_tokens`、`length` 和未知原因，断言最后一个事件：

```go
end := collectTurnEnd(t, runner.Run(messages))
if end.Status != TurnIncomplete || end.StopReason != StopMaxTokens {
    t.Fatalf("turn end = %+v", end)
}
```

- [ ] **步骤 2：编写错误退出只有一个终止事件的失败测试**

覆盖 stream error、`context.Canceled`、`context.DeadlineExceeded`、非法 Agent 配置和迭代耗尽；遍历事件并断言 `TurnEndEvent` 数量等于 1，且事件后 channel 关闭。

- [ ] **步骤 3：运行测试确认红灯**

运行：`go test ./internal/agent -run 'TestAgentTurn|TestAgentStop'`

预期：FAIL，原因是 `TurnEndEvent`、`TurnStatus` 和稳定停止原因尚未定义。

- [ ] **步骤 4：实现最小生命周期模型**

在 `events.go` 添加：

```go
type TurnStatus string
type StopReason string

type TurnEndEvent struct {
    Status         TurnStatus
    StopReason     StopReason
    ProviderReason string
    Usage          llm.UsageInfo
    Err            error
}
```

在 `agent.go` 添加 `turnEndFromStopReason` 和 `turnEndFromError`，所有 return 前只发送一次 `TurnEndEvent`。终止事件发送必须使用不会因已取消 context 而被丢弃的专用 helper。

- [ ] **步骤 5：适配已有重试测试并运行绿灯**

将已有 `DoneEvent`/`ErrorEvent` 断言改为 `TurnEndEvent`，保留“畸形工具输入只重试一次”的行为。

运行：`go test ./internal/agent`

预期：PASS。

### 任务 2：实现版本化 JSONL 编码器

**文件：**
- 创建：`internal/protocol/event.go`
- 创建：`internal/protocol/encoder.go`
- 创建：`internal/protocol/encoder_test.go`

- [ ] **步骤 1：编写 envelope 与事件映射失败测试**

测试 `TextEvent`、Thinking、全部工具事件和 `TurnEndEvent`，将输出逐行 `json.Unmarshal` 并断言：

```go
if event.Version != 1 || event.Type != "turn_finished" {
    t.Fatalf("event = %+v", event)
}
if event.SessionID != "session-1" || event.TurnID != "turn-1" {
    t.Fatalf("identity = %+v", event)
}
```

- [ ] **步骤 2：编写序列号与单行输出失败测试**

连续编码两个事件，断言 sequence 为 1、2，每个对象只占一行，输出不包含 `\x1b`。

- [ ] **步骤 3：运行测试确认红灯**

运行：`go test ./internal/protocol`

预期：FAIL，原因是 protocol package 尚不存在。

- [ ] **步骤 4：实现最小 encoder**

定义 envelope：

```go
type Event struct {
    Version   int    `json:"version"`
    Sequence  uint64 `json:"sequence"`
    Type      string `json:"type"`
    SessionID string `json:"session_id"`
    TurnID    string `json:"turn_id"`
    Data      any    `json:"data"`
}
```

`Encoder` 保存 writer 和 sequence；`EncodeAgentEvent(sessionID, turnID, event)` 负责类型映射，`EncodeTurnStarted` 负责起始事件。未知 AgentEvent 返回错误，禁止静默丢失。

- [ ] **步骤 5：运行编码器测试确认绿灯**

运行：`go test ./internal/protocol`

预期：PASS。

### 任务 3：实现 JSONL 机器运行器

**文件：**
- 创建：`internal/ui/jsonl/run.go`
- 创建：`internal/ui/jsonl/run_test.go`

- [ ] **步骤 1：编写按行多 Turn 的失败测试**

使用 `strings.NewReader("first\n\nsecond\n")`，注入 fake TurnRunner 和真实 protocol Encoder，断言产生两个 `turn_started` 与两个 `turn_finished`，空行被忽略。

- [ ] **步骤 2：编写失败与异常关闭测试**

一项测试让 runner 返回 `TurnFailed`，断言进程仍读取下一行；另一项让事件 channel 无终止事件关闭，断言合成 `status=failed`、`stop_reason=agent_error` 的 `turn_finished`。

- [ ] **步骤 3：运行测试确认红灯**

运行：`go test ./internal/ui/jsonl`

预期：FAIL，原因是 JSONL runner 尚不存在。

- [ ] **步骤 4：实现可测试的运行边界**

定义最小接口：

```go
type TurnRunner interface {
    RunContext(context.Context, *conversation.MessageManager) <-chan agent.AgentEvent
}
```

Runtime 接收 `io.Reader`、`io.Writer`、Conversation Service、TurnRunner 和 session-change callback。每个非空输入保存用户消息、生成进程内 turn ID、输出起始事件并转发 AgentEvent。

- [ ] **步骤 5：运行 JSONL 测试确认绿灯**

运行：`go test ./internal/ui/jsonl`

预期：PASS，stdout 每行均可被 `json.Unmarshal`。

### 任务 4：接入 CLI 并保持终端兼容

**文件：**
- 修改：`internal/ui/terminal/repl.go`
- 修改：`internal/ui/terminal/repl_test.go`
- 修改：`internal/app/options.go`
- 修改：`internal/app/options_test.go`
- 修改：`internal/app/app.go`
- 修改：`internal/app/README.md`
- 修改：`cmd/FFCode/README.md`

- [ ] **步骤 1：编写 CLI 参数失败测试**

断言：

```go
options, _ := parseOptions(nil)
if options.OutputFormat != "text" { t.Fatalf(...) }
```

并覆盖 `--output-format jsonl` 与非法值 `xml`。

- [ ] **步骤 2：编写终端 TurnEndEvent 渲染失败测试**

完成事件应输出 token 和 `done: end_turn`；失败事件应返回对应错误，但不能依赖模型正文中的 `done:`。

- [ ] **步骤 3：运行相关测试确认红灯**

运行：`go test ./internal/app ./internal/ui/terminal`

预期：FAIL，原因是 Options 尚无 OutputFormat，renderer 尚不识别 TurnEndEvent。

- [ ] **步骤 4：实现 CLI 装配**

在 Options 增加 OutputFormat，flag 默认 `text` 并显式校验。`app.Run` 在 `jsonl` 时调用 JSONL runner，在 `text` 时调用现有 terminal runner；更新 help 与 README。

- [ ] **步骤 5：运行相关测试确认绿灯**

运行：`go test ./internal/app ./internal/ui/terminal ./internal/ui/jsonl`

预期：PASS。

### 任务 5：迁移现有自动化协议消费者

**文件：**
- 修改：`benchmark/swebench-lite-20260726/run.py`
- 创建：`benchmark/swebench-lite-20260726/test_run.py`

- [ ] **步骤 1：编写 JSONL 状态解析失败测试**

测试普通 `text_delta` 中含 `done:` 时不结束；只有 `turn_finished` 结束，并把 `completed`、`incomplete`、`failed`、`cancelled` 映射到独立执行状态。

- [ ] **步骤 2：运行 Python 测试确认红灯**

运行：`python3 -m unittest benchmark/swebench-lite-20260726/test_run.py`

预期：FAIL，原因是 JSONL 解析 helper 尚不存在。

- [ ] **步骤 3：实现协议消费**

启动命令增加 `--output-format jsonl`。逐行解析 JSON；忽略未知的非终止事件；记录 `turn_finished.data.status` 和 `stop_reason`；删除 `"done:" in text` 与中文错误文案检测。

- [ ] **步骤 4：运行 Python 测试确认绿灯**

运行：`python3 -m unittest discover -s benchmark/swebench-lite-20260726 -p 'test_*.py'`

预期：PASS。

### 任务 6：格式化与完整验证

**文件：**
- 检查全部本计划修改文件

- [ ] **步骤 1：格式化 Go 文件**

运行：`gofmt -w internal/agent/*.go internal/protocol/*.go internal/ui/jsonl/*.go internal/ui/terminal/repl.go internal/ui/terminal/repl_test.go internal/app/*.go`

- [ ] **步骤 2：运行完整 Go 测试**

运行：`go test ./...`

预期：PASS，0 failed。

- [ ] **步骤 3：运行并发相关 race 测试**

运行：`go test -race ./internal/agent ./internal/protocol ./internal/ui/jsonl ./internal/ui/terminal`

预期：PASS，未报告 data race。

- [ ] **步骤 4：运行 Python 回归测试**

运行：`python3 -m unittest discover -s benchmark/swebench-lite-20260726 -p 'test_*.py'`

预期：PASS。

- [ ] **步骤 5：构建 CLI 并检查帮助**

运行：`go build ./cmd/FFCode && ./FFCode --help`

预期：构建成功，帮助中显示 `--output-format text|jsonl`。

- [ ] **步骤 6：检查 diff**

运行：`git diff --check` 和 `git status --short`，确认没有格式错误、生成物或无关文件变更。

由于开始实现前工作区已有用户修改，执行过程中不自动提交生产代码；最终由用户决定如何拆分或提交。
