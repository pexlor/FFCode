# 跨会话记忆实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [x]`）语法来跟踪进度。

**目标：** 为 MyCode 实现可恢复会话、项目知识加载、自动记忆抽取与整合，并把受预算约束的长期记忆注入模型上下文。

**架构：** 保留 `fileconversation` 作为 Transcript 事实源，新增 `memory` 领域包和 `filememory` 文件后端。后台 Service 使用 Phase 1 会话抽取和 Phase 2 全局整合，`ContextManager` 只通过只读 SummaryProvider 消费已生效摘要。

**技术栈：** Go 1.25、标准库 JSON/JSONL/文件锁、现有 LLM Client、Go testing

---

## 文件结构

- 创建 `internal/memory/types.go`：记忆数据模型、状态、错误和接口。
- 创建 `internal/memory/validate.go`：结构化输出校验、规范化和确定性合并。
- 创建 `internal/memory/knowledge.go`：`AGENTS.md`、`RULES.md` 和 `@include` 加载。
- 创建 `internal/memory/extractor.go`：Phase 1 LLM 请求和 JSON 输出解析。
- 创建 `internal/memory/consolidator.go`：Phase 2 LLM 请求、确定性降级和 Markdown 渲染。
- 创建 `internal/memory/service.go`：后台扫描、任务认领、抽取和整合编排。
- 创建 `internal/storage/filememory/store.go`：原始记忆、任务租约、快照和 Manifest 原子文件实现。
- 修改 `internal/storage/fileconversation/store.go`：有效前缀恢复和完整 Turn 截断。
- 修改 `internal/context/loader.go`：加载显式项目知识。
- 修改 `internal/context/manager.go`：注入长期记忆摘要。
- 修改 `internal/config/config.go`、`internal/config/loader_test.go`：Memory 配置与默认值。
- 修改 `internal/app/bootstrap.go`、`internal/app/app.go`：创建、启动和关闭 Memory Service。
- 为上述模块创建同目录 `_test.go` 测试。

### 任务 1：领域模型与文件存储

**文件：**
- 创建：`internal/memory/types.go`
- 创建：`internal/memory/validate.go`
- 创建：`internal/storage/filememory/store.go`
- 测试：`internal/memory/validate_test.go`
- 测试：`internal/storage/filememory/store_test.go`

- [x] **步骤 1：编写失败测试**

覆盖 RawMemory Evidence 校验、幂等追加、抽取租约 Token、整合全局租约、Snapshot 乐观锁与 Active Summary。

- [x] **步骤 2：运行测试验证失败**

运行：`go test ./internal/memory ./internal/storage/filememory`

预期：FAIL，包或类型尚不存在。

- [x] **步骤 3：实现最少领域接口和文件存储**

核心接口：

```go
type Store interface {
    ClaimExtraction(context.Context, ExtractionCandidate, string, time.Duration) (ExtractionClaim, error)
    CompleteExtraction(context.Context, ExtractionClaim, RawMemory) error
    FailExtraction(context.Context, ExtractionClaim, string, time.Time) error
    ListConsolidationInputs(context.Context, int, time.Time) ([]RawMemory, error)
    ClaimConsolidation(context.Context, string, time.Duration) (ConsolidationClaim, error)
    CommitSnapshot(context.Context, ConsolidationClaim, int, MemorySnapshot) error
    ActiveSnapshot(context.Context) (*MemorySnapshot, error)
}
```

- [x] **步骤 4：运行测试验证通过**

运行：`go test ./internal/memory ./internal/storage/filememory`

预期：PASS。

- [x] **步骤 5：Commit**

```bash
git add internal/memory internal/storage/filememory
git commit -m "feat(memory): add durable memory store"
```

### 任务 2：项目知识安全加载

**文件：**
- 创建：`internal/memory/knowledge.go`
- 测试：`internal/memory/knowledge_test.go`
- 修改：`internal/context/loader.go`
- 修改：`internal/context/workspace_test.go`

- [x] **步骤 1：编写失败测试**

覆盖根/深层规则顺序、相对 Include、循环、Workspace 越界、符号链接、代码块内 Include 和大小上限。

- [x] **步骤 2：运行测试验证失败**

运行：`go test ./internal/memory ./internal/context`

预期：FAIL，KnowledgeLoader 未定义或规则内容不匹配。

- [x] **步骤 3：实现加载器并接入 DemandLoader**

```go
type KnowledgeLoader struct {
    Workspace string
    MaxDepth int
    MaxFiles int
    MaxFileBytes int64
    MaxTotalBytes int64
}

func (l KnowledgeLoader) Load(activePaths []string) ([]Document, error)
```

- [x] **步骤 4：运行测试验证通过**

运行：`go test ./internal/memory ./internal/context`

预期：PASS。

- [x] **步骤 5：Commit**

```bash
git add internal/memory/knowledge.go internal/memory/knowledge_test.go internal/context
git commit -m "feat(memory): load project knowledge safely"
```

### 任务 3：可靠 Transcript 恢复

**文件：**
- 修改：`internal/storage/fileconversation/store.go`
- 修改：`internal/conversation/service.go`
- 测试：`internal/storage/fileconversation/store_test.go`
- 测试：`internal/conversation/service_test.go`

- [x] **步骤 1：编写失败测试**

覆盖最后一行半写入恢复、中央损坏只恢复有效前缀、未完成 Turn 不进入恢复 History、恢复首轮时间跨度提示。

- [x] **步骤 2：运行测试验证失败**

运行：`go test ./internal/storage/fileconversation ./internal/conversation`

预期：FAIL，当前 Store 在任意非法 JSON 行直接返回错误。

- [x] **步骤 3：实现有效前缀恢复和完整 Turn 过滤**

`ListMessages` 返回可验证前缀；`Resume` 只恢复截至最后 `TurnComplete` 消息，并添加带上次活动时间的边界消息。

- [x] **步骤 4：运行测试验证通过**

运行：`go test ./internal/storage/fileconversation ./internal/conversation`

预期：PASS。

- [x] **步骤 5：Commit**

```bash
git add internal/storage/fileconversation internal/conversation
git commit -m "feat(conversation): recover complete transcript prefix"
```

### 任务 4：Phase 1 抽取

**文件：**
- 创建：`internal/memory/extractor.go`
- 创建：`internal/memory/extractor_test.go`
- 创建：`internal/memory/service.go`
- 创建：`internal/memory/service_test.go`

- [x] **步骤 1：编写失败测试**

使用 Fake Extractor 覆盖 Session 候选筛选、Transcript Hash 幂等、空输出、失败退避和并发上限；使用 Fake LLM 覆盖严格 JSON 解析和非法 Evidence 拒绝。

- [x] **步骤 2：运行测试验证失败**

运行：`go test ./internal/memory`

预期：FAIL，Extractor 和 Service 尚不存在。

- [x] **步骤 3：实现抽取器和有界后台编排**

```go
type TranscriptSource interface {
    ListSessions(context.Context, string, int) ([]conversation.SessionMetadata, error)
    ListMessages(context.Context, string) ([]conversation.StoredMessage, error)
}
```

每次 `RunOnce` 有界扫描并处理候选；应用层可以定时调用而不阻塞主对话。

- [x] **步骤 4：运行测试验证通过**

运行：`go test ./internal/memory`

预期：PASS。

- [x] **步骤 5：Commit**

```bash
git add internal/memory
git commit -m "feat(memory): extract memories from idle sessions"
```

### 任务 5：Phase 2、摘要注入与配置接线

**文件：**
- 创建：`internal/memory/consolidator.go`
- 创建：`internal/memory/consolidator_test.go`
- 修改：`internal/context/manager.go`
- 修改：`internal/context/workspace_test.go`
- 修改：`internal/config/config.go`
- 修改：`internal/config/loader_test.go`
- 修改：`internal/app/bootstrap.go`
- 修改：`internal/app/app.go`

- [x] **步骤 1：编写失败测试**

覆盖确定性去重、冲突降级、摘要 Token 上限、Prompt 优先级、`generate/use` 独立开关、后台启动和关闭。

- [x] **步骤 2：运行测试验证失败**

运行：`go test ./internal/memory ./internal/context ./internal/config ./internal/app`

预期：FAIL，Consolidator、SummaryProvider 和配置字段不存在。

- [x] **步骤 3：实现整合、读取边界和 Bootstrap**

```go
type SummaryProvider interface {
    Summary(context.Context) (string, error)
}
```

`ContextManager` 在项目规则之后追加 `[cross-session memory]`；读取失败降级为空摘要。Bootstrap 在启用 Generate 时启动后台 Service，并在 Cleanup 时取消和等待。

- [x] **步骤 4：运行测试验证通过**

运行：`go test ./internal/memory ./internal/context ./internal/config ./internal/app`

预期：PASS。

- [x] **步骤 5：Commit**

```bash
git add internal/memory internal/context internal/config internal/app
git commit -m "feat(memory): consolidate and inject cross-session memory"
```

### 任务 6：文档、全量验证与竞争测试

**文件：**
- 修改：`internal/memory/README.md`
- 修改：`internal/memory/DESIGN.md`
- 修改：`internal/config/README.md`

- [x] **步骤 1：更新文档为实现状态**

记录实际文件、配置默认值、已实现限制和操作方式，删除与实现不一致的描述。

- [x] **步骤 2：格式化与静态检查**

运行：`gofmt -w <changed-go-files>`、`go vet ./...`、`git diff --check`

预期：全部退出码为 0。

- [x] **步骤 3：运行全量测试**

运行：`go test ./...`

预期：全部 PASS。

- [x] **步骤 4：运行竞争测试**

运行：`go test -race ./internal/memory/... ./internal/storage/filememory/... ./internal/context/... ./internal/conversation/...`

预期：全部 PASS，无 race report。

- [x] **步骤 5：Commit**

```bash
git add internal/memory internal/config/README.md docs/superpowers/plans/2026-07-26-memory-pipeline.md
git commit -m "docs(memory): document memory pipeline"
```
