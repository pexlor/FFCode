# 终端多行输入与单轮计时实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 让交互式终端支持 `Ctrl+Enter` 换行、`Enter` 发送的自适应多行输入，并实时显示当前单轮 Agent loop 的耗时。

**架构：** 使用 Bubbles `textarea.Model` 替换现有 `textinput.Model`，由组件负责多行光标、软换行和 1 至 8 行动态高度。计时复用现有事件消费循环和瞬时状态行，通过可停止 ticker 每秒刷新，终态输出保留 100ms 精度耗时。

**技术栈：** Go 1.25、Bubble Tea v2、Bubbles textarea、Lip Gloss、Go `testing`。

---

## 文件结构

- 修改 `internal/ui/terminal/input.go`：迁移 textarea、定义按键语义、动态高度和多行历史边界。
- 创建 `internal/ui/terminal/input_test.go`：覆盖多行编辑、提交、动态高度和历史导航。
- 修改 `internal/ui/terminal/repl.go`：测量单轮耗时、驱动实时状态刷新、渲染最终耗时。
- 修改 `internal/ui/terminal/repl_test.go`：覆盖计时刷新、完成、中断和失败生命周期。
- 修改 `internal/ui/terminal/README.md`：记录多行输入和实时 loop 计时职责。

### 任务 1：自适应多行输入

**文件：**
- 修改：`internal/ui/terminal/input.go`
- 创建：`internal/ui/terminal/input_test.go`

- [ ] **步骤 1：编写失败的输入交互测试**

创建按键辅助函数，通过 `tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl})` 发送 `Ctrl+Enter`，断言值变为 `first\nsecond` 且 `submitted == false`；再发送无修饰 `Enter`，断言提交完整值。增加宽度较小时软换行自动增高、超过八行后 `Height() == 8` 的断言。

- [ ] **步骤 2：运行测试并确认红灯**

运行：`go test ./internal/ui/terminal -run 'TestPromptModel(CtrlEnter|Enter|DynamicHeight)' -count=1`

预期：FAIL，因为当前 `textinput.Model` 不接受换行，也没有动态高度。

- [ ] **步骤 3：实现 textarea 最小迁移**

将输入字段改为 `textarea.Model`，关闭行号，保留现有颜色和真实光标，并设置：

```go
input.DynamicHeight = true
input.MinHeight = 1
input.MaxHeight = 8
input.MaxContentHeight = 10_000
input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+enter"))
```

在 `promptModel.Update` 中继续拦截普通 `enter` 进行提交，让 `ctrl+enter` 进入 textarea 更新。删除单行输入专用的水平 viewport 与光标偏移代码，直接使用 `textarea.Cursor()`。

- [ ] **步骤 4：补充历史边界与命令补全测试并实现**

测试多行中间位置的 `Up`/`Down` 只移动光标，第一/最后视觉行才浏览历史；测试 `/` 命令提示仍可用 `Tab` 和 `Enter` 确认。使用 `Line()`、`LineCount()` 和 `LineInfo()` 判断第一/最后视觉行。

- [ ] **步骤 5：格式化并验证输入测试**

运行：`gofmt -w internal/ui/terminal/input.go internal/ui/terminal/input_test.go`

运行：`go test ./internal/ui/terminal -run 'TestPromptModel' -count=1`

预期：PASS。

### 任务 2：实时单轮 loop 计时

**文件：**
- 修改：`internal/ui/terminal/repl.go`
- 修改：`internal/ui/terminal/repl_test.go`
- 修改：`internal/ui/terminal/README.md`

- [ ] **步骤 1：编写失败的计时渲染测试**

为 renderer 增加测试期望：状态行在 elapsed 更新后包含 `正在思考` 和 `12s`；终态 usage 行包含 `elapsed: 18.4s`。为事件消费循环注入可控 tick channel，断言没有 Agent 事件时 tick 仍刷新状态，并在事件流关闭后调用 ticker 的 `Stop`。

- [ ] **步骤 2：运行测试并确认红灯**

运行：`go test ./internal/ui/terminal -run 'Test(AgentEventRendererElapsed|ConsumeAgentEvents.*Elapsed)' -count=1`

预期：FAIL，因为 renderer 和事件循环尚未保存或刷新 elapsed。

- [ ] **步骤 3：实现可停止的 loop ticker**

在调用 `Runner.RunContext` 前记录 `startedAt`。事件消费循环同时 select Agent 事件、中断信号和每秒 tick；每个事件或 tick 都以 `now.Sub(startedAt)` 更新 renderer。ticker 由消费函数拥有并用 `defer Stop()` 保证所有返回路径释放。

- [ ] **步骤 4：实现状态行和终态耗时渲染**

renderer 保存当前瞬时状态与 elapsed；重绘格式为 `<状态> · <整秒>s`。`TurnEndEvent` 的 usage 行增加 `elapsed: <100ms 精度>`；中断和渲染失败路径也在返回前输出最终 elapsed。

- [ ] **步骤 5：更新终端文档并验证包测试**

在 `internal/ui/terminal/README.md` 记录 `Ctrl+Enter`、自适应高度和实时 loop 计时。

运行：`gofmt -w internal/ui/terminal/repl.go internal/ui/terminal/repl_test.go`

运行：`go test ./internal/ui/terminal -count=1`

预期：PASS。

### 任务 3：全量验证

**文件：**
- 检查：`internal/ui/terminal/input.go`
- 检查：`internal/ui/terminal/input_test.go`
- 检查：`internal/ui/terminal/repl.go`
- 检查：`internal/ui/terminal/repl_test.go`
- 检查：`internal/ui/terminal/README.md`

- [ ] **步骤 1：检查变更范围**

运行：`git diff -- internal/ui/terminal docs/superpowers/plans/2026-07-30-terminal-input-and-loop-timing.md`

确认没有覆盖用户在 `TODO.md`、`internal/agent` 和 `internal/tool` 中的未提交改动。

- [ ] **步骤 2：运行全量测试**

运行：`go test ./...`

预期：所有包 PASS。

- [ ] **步骤 3：仅在实现引入额外 goroutine 时运行竞态测试**

运行：`go test -race ./internal/ui/terminal`

预期：PASS。若 ticker 完全由现有同步 select 循环拥有且未新增 goroutine，则记录无需执行该项的理由。
