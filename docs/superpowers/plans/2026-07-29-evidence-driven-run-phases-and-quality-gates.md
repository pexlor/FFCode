# 证据驱动阶段判定与告警式质量门禁实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 使用工作区变化和验证结果驱动 Explore、Implement、Verify、Finalize 阶段，并在证据不足时输出不阻断执行的结构化质量告警。

**架构：** 新增独立的验证分类器、Git 变化检测器、Run 证据聚合器和质量规则评估器；Agent 主循环只在既定边界提交观察结果并发布事件。告警不进入会话历史、不改变 Turn 终止状态，JSONL 协议保持版本 1。

**技术栈：** Go 1.25、标准库 `os/exec` 和 `crypto/sha256`、现有 Agent/Tool/Protocol/Terminal 事件体系。

---

## 文件结构

**创建：**

- `internal/agent/verification.go`、`verification_test.go`：验证命令分类。
- `internal/agent/evidence.go`、`evidence_test.go`：Run 证据和观察顺序。
- `internal/agent/change_detector.go`、`change_detector_test.go`：有界 Git 快照。
- `internal/agent/quality_gate.go`、`quality_gate_test.go`：告警规则和去重。

**修改：**

- `internal/agent/run_phase.go`、`run_phase_test.go`：证据驱动状态机。
- `internal/agent/agent.go`、`events.go`、`progress.go`：主循环接入。
- `internal/protocol/event.go`、`encoder.go`、`encoder_test.go`：JSONL 事件。
- `internal/ui/jsonl/run_test.go`：事件顺序。
- `internal/ui/terminal/repl.go`、`repl_test.go`：终端呈现。
- `internal/agent/README.md`、`internal/protocol/README.md`：行为契约。

### 任务 1：验证命令分类器

**文件：**

- 创建：`internal/agent/verification.go`
- 创建：`internal/agent/verification_test.go`
- 修改：`internal/agent/run_phase.go`

- [ ] **步骤 1：编写失败的分类矩阵测试**

```go
func TestDefaultVerificationClassifier(t *testing.T) {
	tests := []struct {
		command string
		want    VerificationScope
		ok      bool
	}{
		{"go test ./internal/agent", VerificationPackage, true},
		{"go test ./...", VerificationFull, true},
		{"python -m pytest tests/test_api.py -q", VerificationFocused, true},
		{"python tests/runtests.py auth_tests", VerificationFocused, true},
		{"npm test", VerificationFull, true},
		{"cargo test parser", VerificationFocused, true},
		{"git diff --check", VerificationFallback, true},
		{"python -m py_compile module.py", VerificationFallback, true},
		{"git status --short", VerificationUnknown, false},
	}
	classifier := defaultVerificationClassifier{}
	for _, test := range tests {
		got, ok := classifier.Classify(llm.ToolCallComplete{
			ToolName: "Bash", Arguments: map[string]any{"command": test.command},
		})
		if got != test.want || ok != test.ok {
			t.Fatalf("Classify(%q) = %q, %t; want %q, %t", test.command, got, ok, test.want, test.ok)
		}
	}
}
```

另加测试证明非 Bash 工具和缺少 `command` 参数返回 `VerificationUnknown, false`。

- [ ] **步骤 2：运行测试，确认因缺少类型失败**

```bash
go test ./internal/agent -run 'Test(DefaultVerificationClassifier|VerificationClassifierRejects)' -count=1
```

预期：编译失败，提示 `VerificationScope` 或 `defaultVerificationClassifier` 未定义。

- [ ] **步骤 3：实现最小分类接口**

```go
type VerificationScope string

const (
	VerificationUnknown VerificationScope = "unknown"
	VerificationFocused VerificationScope = "focused"
	VerificationPackage VerificationScope = "package"
	VerificationFull VerificationScope = "full"
	VerificationFallback VerificationScope = "fallback"
)

type VerificationClassifier interface {
	Classify(llm.ToolCallComplete) (VerificationScope, bool)
}

type defaultVerificationClassifier struct{}
```

实现顺序为 fallback、full、package、focused，避免 `go test ./...` 被较宽规则提前匹配。暂时保留 `run_phase.go` 中的 `isVerificationCall()`，把函数体改为委托 `defaultVerificationClassifier.Classify()`；任务 4 替换旧状态机调用点后再删除 helper，保证当前提交可独立构建。

- [ ] **步骤 4：格式化、验证并提交**

```bash
gofmt -w internal/agent/verification.go internal/agent/verification_test.go internal/agent/run_phase.go
go test ./internal/agent -run 'Test(DefaultVerificationClassifier|VerificationClassifierRejects)' -count=1
git add internal/agent/verification.go internal/agent/verification_test.go internal/agent/run_phase.go
git commit -m "feat(agent): classify verification commands"
```

### 任务 2：Run 证据和告警规则

**文件：**

- 创建：`internal/agent/evidence.go`
- 创建：`internal/agent/evidence_test.go`
- 创建：`internal/agent/quality_gate.go`
- 创建：`internal/agent/quality_gate_test.go`

- [ ] **步骤 1：编写证据时间顺序失败测试**

```go
func TestRunEvidenceTracksVerificationRelativeToChanges(t *testing.T) {
	evidence := newRunEvidence()
	evidence.RecordVerification(VerificationEvidence{ToolUseID: "baseline", Scope: VerificationFull, Passed: true})
	evidence.RecordChanges([]WorkspaceChange{{Path: "internal/agent/agent.go", Kind: ChangeSource, Operation: ChangeModified}})
	evidence.RecordVerification(VerificationEvidence{ToolUseID: "test-1", Scope: VerificationPackage, Passed: true})
	if evidence.Verifications[0].AfterPatch || !evidence.Verifications[1].AfterPatch {
		t.Fatalf("verifications = %+v", evidence.Verifications)
	}
	evidence.RecordChanges([]WorkspaceChange{{Path: "internal/agent/run_phase.go", Kind: ChangeSource, Operation: ChangeModified}})
	if evidence.LastChangeRevision <= evidence.LastVerificationRevision {
		t.Fatal("new change did not invalidate verification")
	}
}
```

- [ ] **步骤 2：编写 `QG001` 至 `QG008` 表驱动失败测试**

每条规则构造最小 `RunEvidence`。断言命中对应代码，并再次调用 `Evaluate` 验证相同代码和证据不会重复返回。必须额外断言：只有文档变化不产生 `QG001`；一次失败验证后又成功不产生 `QG002`；warnings 的路径按字典序排列。

- [ ] **步骤 3：运行测试并确认缺少 API**

```bash
go test ./internal/agent -run 'Test(RunEvidence|QualityGate)' -count=1
```

- [ ] **步骤 4：实现证据模型**

```go
type WorkspaceChange struct {
	Path string
	Kind ChangeKind
	Operation ChangeOperation
	TestExpectationChanged bool
}

type VerificationEvidence struct {
	ToolUseID string
	Command string
	Scope VerificationScope
	Passed bool
	AfterPatch bool
	Revision uint64
}

type RunEvidence struct {
	Changes []WorkspaceChange
	Verifications []VerificationEvidence
	DiffAvailable bool
	ToolExecutions int
	FinalRequested bool
	SoftBudgetHit bool
	LastChangeRevision uint64
	LastVerificationRevision uint64
	revision uint64
}
```

`RecordChanges` 只在变化集合相对上次观察发生变化时增加 revision；`RecordVerification` 复制输入并基于当前 revision 设置 `AfterPatch`。所有 slice 写入前复制，禁止保留调用方可变引用。

- [ ] **步骤 5：实现质量规则和去重**

```go
type WarningSeverity string
const WarningSeverityWarning WarningSeverity = "warning"

type QualityWarning struct {
	Code string
	Severity WarningSeverity
	Message string
	Evidence []string
}

type qualityGate struct {
	emitted map[string]struct{}
}
```

`Evaluate` 固定按 `QG001` 至 `QG008` 排序。去重键由告警代码和已排序 evidence 的 SHA-256 组成。`QG008` 门槛固定为 20 个实际执行工具。`QG004` 的 message 必须表述为“需要复核”，不能断言测试修改错误。

- [ ] **步骤 6：格式化、验证并提交**

```bash
gofmt -w internal/agent/evidence.go internal/agent/evidence_test.go internal/agent/quality_gate.go internal/agent/quality_gate_test.go
go test ./internal/agent -run 'Test(RunEvidence|QualityGate)' -count=1
git add internal/agent/evidence.go internal/agent/evidence_test.go internal/agent/quality_gate.go internal/agent/quality_gate_test.go
git commit -m "feat(agent): evaluate advisory quality gates"
```

### 任务 3：有界 Git 变化检测

**文件：**

- 创建：`internal/agent/change_detector.go`
- 创建：`internal/agent/change_detector_test.go`

- [ ] **步骤 1：编写临时 Git 仓库失败测试**

使用 `t.TempDir()` 创建仓库并完成初始提交。baseline 前先制造一个脏文件，baseline 后再次修改它并新增 `_test.go`，断言两者都是本 Run 变化。另写测试覆盖：修改后恢复不产生变化、删除测试文件、超出 diff 上限时 `Complete=false`、非 Git 目录返回可识别错误。

```go
detector := newGitChangeDetector(2 << 20)
baseline, err := detector.Snapshot(context.Background(), repo)
if err != nil { t.Fatal(err) }
// mutate files here
current, err := detector.Snapshot(context.Background(), repo)
if err != nil { t.Fatal(err) }
report, err := detector.Compare(baseline, current)
if err != nil { t.Fatal(err) }
assertChange(t, report.Changes, "new_test.go", ChangeAdded, ChangeTest)
```

- [ ] **步骤 2：运行测试并确认缺少检测器**

```bash
go test ./internal/agent -run TestGitChangeDetector -count=1
```

- [ ] **步骤 3：实现接口和有界快照**

```go
type WorkspaceSnapshot struct {
	Root string
	Files map[string]workspaceFileState
	PatchHash string
	Complete bool
}

type ChangeReport struct {
	Changes []WorkspaceChange
	Complete bool
}

type ChangeDetector interface {
	Snapshot(context.Context, string) (WorkspaceSnapshot, error)
	Compare(WorkspaceSnapshot, WorkspaceSnapshot) (ChangeReport, error)
}
```

默认实现执行 `git status --porcelain=v1 -z --untracked-files=all`，为候选路径记录状态、工作树内容哈希和 index 内容哈希。正确消费 rename 的第二个 NUL 路径。Git 命令全部使用 `exec.CommandContext`，stdout/stderr 均有字节上限。

比较结果按路径排序。路径分类顺序是 test、docs、source、config、unknown。`QG004` 启发式仅检查 test 路径的有界 diff：删除断言/期望行或删除测试文件时设置 `TestExpectationChanged`。

- [ ] **步骤 4：格式化、运行普通及竞态测试并提交**

```bash
gofmt -w internal/agent/change_detector.go internal/agent/change_detector_test.go
go test ./internal/agent -run TestGitChangeDetector -count=1
go test -race ./internal/agent -run TestGitChangeDetector -count=1
git add internal/agent/change_detector.go internal/agent/change_detector_test.go
git commit -m "feat(agent): detect run-attributed workspace changes"
```

### 任务 4：证据驱动状态机与 Agent 接入

**文件：**

- 修改：`internal/agent/run_phase.go`
- 修改：`internal/agent/run_phase_test.go`
- 修改：`internal/agent/agent.go`
- 修改：`internal/agent/events.go`
- 修改：`internal/agent/evidence.go`
- 修改：`internal/agent/progress.go`

- [ ] **步骤 1：编写状态机失败测试**

```go
func TestRunPhaseControllerUsesEvidenceAndAllowsRework(t *testing.T) {
	controller := newRunPhaseController()
	if got := controller.observe(phaseObservation{WorkspaceChanged: true}); got.To != PhaseImplement { t.Fatalf("%+v", got) }
	if got := controller.observe(phaseObservation{VerificationAttempted: true}); got.To != PhaseVerify { t.Fatalf("%+v", got) }
	if got := controller.observe(phaseObservation{WorkspaceChanged: true}); got.To != PhaseImplement { t.Fatalf("%+v", got) }
	if got := controller.observe(phaseObservation{FinalRequested: true}); got.To != PhaseFinalize { t.Fatalf("%+v", got) }
}
```

再写 Agent 集成测试：自定义 Bash 工具在临时 Git 仓库写文件，事件顺序为 Explore、Implement、Verify、Finalize；测试后再次写文件必须从 Verify 回到 Implement。

- [ ] **步骤 2：运行测试并确认旧状态机失败**

```bash
go test ./internal/agent -run 'Test(RunPhaseControllerUsesEvidence|AgentUsesWorkspaceEvidence)' -count=1
```

- [ ] **步骤 3：实现证据观察状态机**

```go
type phaseObservation struct {
	WorkspaceChanged bool
	VerificationAttempted bool
	FinalRequested bool
	SoftBudgetHit bool
}
```

新增 `PhaseReasonWorkspaceChanged`、`PhaseReasonVerificationAttempted`、`PhaseReasonFinalRequested`。移除“验证成功即 Finalize”的规则。Verify 可以回到 Implement；Finalize 保持终态。

- [ ] **步骤 4：实现 run-local 协调器**

```go
func (c *runEvidenceCoordinator) Start(context.Context, string)
func (c *runEvidenceCoordinator) AfterTools(context.Context, string, []llm.ToolCallComplete, []tool.ToolResult) phaseObservation
func (c *runEvidenceCoordinator) BeforeFinalize(context.Context, string, bool) (phaseObservation, []QualityWarning)
func (c *runEvidenceCoordinator) Evidence() RunEvidence
```

`Start` 和后续快照失败只能设置 `DiffAvailable=false`。`AfterTools` 只接收实际执行、未被 progress 拦截的 calls/results。Agent 增加可注入的 `ChangeDetector` 和 `VerificationClassifier` 字段，`NewAgent` 安装默认实现。

主循环顺序：Run 验证后 baseline；工具批次提交后观察；无工具响应时先刷新证据、进入 Finalize、发送告警，再同步 session、保存 checkpoint、发送 TurnEnd。告警绝不追加到 `session.History`。

- [ ] **步骤 5：增加 Agent 告警事件和实际工具计数**

```go
type QualityWarningEvent struct {
	Code string
	Severity WarningSeverity
	Message string
	Evidence []string
}
func (QualityWarningEvent) agentEvent() {}
```

`progressTracker` 增加 `executedToolCount()`，只累计越过执行边界的实际工具；协调器使用该值填充 `QG008` 输入。

- [ ] **步骤 6：格式化、全包测试、竞态测试并提交**

```bash
gofmt -w internal/agent
go test ./internal/agent -count=1
go test -race ./internal/agent -count=1
git add internal/agent/agent.go internal/agent/evidence.go internal/agent/events.go internal/agent/progress.go internal/agent/run_phase.go internal/agent/run_phase_test.go internal/agent/evidence_test.go
git commit -m "feat(agent): drive run phases from workspace evidence"
```

### 任务 5：JSONL 质量告警事件

**文件：**

- 修改：`internal/protocol/event.go`
- 修改：`internal/protocol/encoder.go`
- 修改：`internal/protocol/encoder_test.go`
- 修改：`internal/ui/jsonl/run_test.go`

- [ ] **步骤 1：编写协议和事件顺序失败测试**

在 encoder 的事件表加入 `QualityWarningEvent{Code: "QG001", Severity: "warning"}`，期望 type 为 `quality_warning`。JSONL Runner 测试输入 warning 后跟 `TurnEndEvent`，期望顺序为 `turn_started`、`quality_warning`、`turn_finished`，终止状态仍为 completed。

- [ ] **步骤 2：运行测试并确认 unsupported event**

```bash
go test ./internal/protocol ./internal/ui/jsonl -count=1
```

- [ ] **步骤 3：实现版本 1 编码**

```go
type qualityWarningData struct {
	Code string `json:"code"`
	Severity string `json:"severity"`
	Message string `json:"message"`
	Evidence []string `json:"evidence,omitempty"`
}
```

在 `agentEventData()` 增加 case，类型固定为 `quality_warning`。不要修改 `protocol.Version`、`turnFinishedData` 或 JSONL terminal detection。

- [ ] **步骤 4：格式化、验证并提交**

```bash
gofmt -w internal/protocol internal/ui/jsonl
go test ./internal/protocol ./internal/ui/jsonl -count=1
git add internal/protocol/event.go internal/protocol/encoder.go internal/protocol/encoder_test.go internal/ui/jsonl/run_test.go
git commit -m "feat(protocol): emit advisory quality warnings"
```

### 任务 6：终端、文档和全量验证

**文件：**

- 修改：`internal/ui/terminal/repl.go`
- 修改：`internal/ui/terminal/repl_test.go`
- 修改：`internal/agent/README.md`
- 修改：`internal/protocol/README.md`

- [ ] **步骤 1：编写终端渲染失败测试**

复用 `repl_test.go` 的 renderer 构造方式，渲染 `QualityWarningEvent`，断言输出包含 `Quality warning QG001:` 和证据路径，同时 `render()` 返回 nil。

- [ ] **步骤 2：运行测试并确认缺少告警输出**

```bash
go test ./internal/ui/terminal -run TestQualityWarningRendersWithoutFailingTurn -count=1
```

- [ ] **步骤 3：实现终端呈现并更新文档**

在现有 event type switch 中增加告警分支，复用 warning 样式，禁止设置 terminal error。Agent README 说明 Finalize 不代表验证成功、Verify 可回到 Implement、Git 降级和告警不阻断。Protocol README 记录 wire shape、事件顺序和版本兼容。

- [ ] **步骤 4：执行完整验证**

```bash
gofmt -w internal/agent internal/protocol internal/ui/jsonl internal/ui/terminal
go test ./...
go test -race ./internal/agent/... ./internal/tool/... ./internal/ui/...
go vet ./...
git diff --check
```

预期：全部成功。竞态测试失败时必须修复，并从 `go test ./...` 开始重新执行整组验证。

- [ ] **步骤 5：提交终端与文档并检查状态**

```bash
git add internal/ui/terminal/repl.go internal/ui/terminal/repl_test.go internal/agent/README.md internal/protocol/README.md
git commit -m "docs(agent): describe evidence-driven quality warnings"
git status --short
git log -6 --oneline
```

预期：无未提交实现文件；最近六个提交对应本计划六项任务。SWE-bench 28-case replay 是单独的长时评测任务，不在本实现计划中自动启动。
