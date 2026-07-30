# 跨会话记忆设计

## 1. 文档状态

- 状态：可实施
- 适用范围：`FFCode` 本地单用户 Agent
- 设计日期：2026-07-26
- 参考实现：`/Users/fengrui03/Desktop/codex/codex-rs/memories`、`codex-rs/state/src/runtime/memories.rs`
- 关联模块：`internal/conversation`、`internal/context`、`internal/storage/fileconversation`、`internal/prompt`

本文把 `README.md` 中的初步想法收敛为可实现的设计。它定义三类长期状态：可恢复的会话、显式项目知识、由历史会话提取并整合的自动记忆。

## 2. 目标与非目标

### 2.1 目标

1. 进程退出或机器重启后，可以恢复完整会话事实，包括用户消息、模型消息、工具调用和工具结果。
2. 在新会话中按需加载项目知识和稳定的跨会话经验，不要求用户重复说明。
3. 自动记忆必须可追溯、可撤销、可去重，并且不能把模型推测升级为事实。
4. 记忆生成在后台执行，失败不阻断当前对话。
5. 所有写入具备崩溃一致性；多个进程同时启动时不会重复抽取或并发覆盖整合结果。
6. 默认使用本地文件存储，保持当前仓库无数据库依赖的技术边界。

### 2.2 非目标

1. 不做语义向量数据库、云端同步或多用户共享。
2. 不把上下文压缩摘要直接当作长期记忆。压缩摘要服务于当前会话的 Token 预算，自动记忆服务于未来会话，生命周期和可信度不同。
3. 不记录密钥、令牌、密码、Cookie、完整环境变量或疑似个人敏感信息。
4. 不允许后台记忆任务修改项目源码、执行项目工具或访问网络。
5. 第一版不自动生成可执行 Skill；只生成 Markdown 记忆和结构化元数据。

## 3. 核心决策

### 3.1 方案比较

| 方案 | 优点 | 缺点 | 结论 |
| --- | --- | --- | --- |
| 每轮直接追加一条 Markdown | 实现最少、立即可读 | 噪声大、重复多、容易把临时信息固化 | 不采用 |
| 每个会话结束后一次性生成全局记忆 | 文件简单 | 并发覆盖、失败难恢复、全量上下文成本高 | 不采用 |
| 两阶段流水线：会话抽取 + 全局整合 | 可并行抽取、可重试、整合串行、来源清晰 | 状态机稍复杂 | 采用 |

两阶段设计参考 Codex，但在当前项目中用文件状态和租约替代数据库任务表：

- Phase 1 从一个已稳定的会话生成独立的原始记忆记录，不直接修改全局记忆。
- Phase 2 在持有全局租约时，把一组原始记忆整合为可注入模型的摘要和可审计的详细记忆。
- 项目知识不参加模型整合。`AGENTS.md` 和 `RULES.md` 始终由用户维护并具有更高优先级。

### 3.2 事实源分离

系统维护四种互不覆盖的事实源：

| 事实源 | 权威内容 | 是否有损 | 写入者 |
| --- | --- | --- | --- |
| Transcript | 完整会话事件 | 否 | 会话运行时 |
| Context Summary | 当前会话压缩检查点 | 是 | `ContextManager` |
| Project Knowledge | 项目说明和规则 | 否 | 用户/项目维护者 |
| Auto Memory | 从历史会话归纳的稳定经验 | 是 | Memory Pipeline |

任何摘要或记忆都不能反向删除、改写 Transcript。出现冲突时，优先级为：当前用户指令 > 当前项目文件事实 > Project Knowledge > Auto Memory > 历史会话摘要。

## 4. 目录布局

会话继续沿用当前实现的 `.context/sessions`。跨会话记忆使用项目内 `.ffcode/memory`，二者不能合并，避免清理记忆时误删会话。

```text
<workspace>/
├── AGENTS.md                         # 可选，项目知识和工作方式
├── RULES.md                          # 可选，编码规则
├── .context/
│   └── sessions/
│       └── <session-id>/
│           ├── manifest.json
│           ├── transcript.jsonl
│           ├── summaries/
│           └── tool-results/
└── .ffcode/
    └── memory/
        ├── manifest.json             # 当前生效版本、最近任务状态
        ├── memory_summary.md         # 默认注入 Prompt 的短摘要
        ├── MEMORY.md                 # 详细、可审计的长期记忆
        ├── raw_memories.jsonl        # Phase 1 成功输出，追加事实源
        ├── sources/
        │   └── <session-id>.json     # 每会话最新抽取状态
        ├── jobs/
        │   ├── extraction/
        │   │   └── <session-id>.json
        │   └── consolidation.json
        ├── leases/
        │   └── consolidation.lock
        ├── snapshots/
        │   └── memory-<version>.json
        └── quarantine/
            └── <timestamp>-<name>    # 损坏或无法解析的文件
```

所有目录权限为 `0700`，文件权限为 `0600`。项目若提交到版本控制，应默认在 `.gitignore` 中排除 `.context/` 和 `.ffcode/memory/`；显式项目知识文件是否提交由项目自行决定。

## 5. 数据模型

### 5.1 会话持久化

现有 `conversation.StoredMessage` 继续作为 Transcript 的权威消息结构。写入约束：

1. 一条 JSON 对象独占一行。
2. 先追加并 `fsync` Transcript，再原子更新 `manifest.json`。
3. `ToolUseID` 必须在同一 Turn 内唯一。
4. 一个 Turn 只有在最终 Assistant 消息持久化后才标记为 `complete`。
5. 大型工具结果可归档为 Artifact，但 Transcript 保留哈希、预览和引用。

### 5.2 原始记忆

```go
type RawMemory struct {
    ID              string        `json:"id"`
    SessionID       string        `json:"session_id"`
    Workspace       string        `json:"workspace"`
    SourceVersion   int           `json:"source_version"`
    TranscriptHash  string        `json:"transcript_hash"`
    Categories      []MemoryItem  `json:"categories"`
    SessionSummary  string        `json:"session_summary"`
    GeneratedAt     time.Time     `json:"generated_at"`
    ExtractorModel  string        `json:"extractor_model"`
    PromptVersion   int           `json:"prompt_version"`
}

type MemoryItem struct {
    Key         string     `json:"key"`
    Kind        MemoryKind `json:"kind"`
    Content     string     `json:"content"`
    Evidence    []Evidence `json:"evidence"`
    Confidence  float64    `json:"confidence"`
    Scope       string     `json:"scope"`
    ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type Evidence struct {
    MessageID string `json:"message_id"`
    TurnID    string `json:"turn_id"`
    Quote     string `json:"quote"`
}
```

`MemoryKind` 只允许以下四类：

- `user_preference`：用户明确表达且可能跨会话稳定的偏好。
- `correction`：用户对 Agent 行为、结论或代码方式的明确纠正。
- `project_fact`：从工具结果或用户陈述确认的项目事实。
- `reference`：用户明确要求后续复用的路径、命令、文档或外部标识。

`Key` 是规范化去重键，格式为 `<kind>/<scope>/<slug>`，例如 `correction/project/use-rg-for-search`。同一 Key 可以有多个来源，但只有一个整合后的当前值。

### 5.3 整合快照

```go
type MemorySnapshot struct {
    Version           int                 `json:"version"`
    PreviousVersion   int                 `json:"previous_version"`
    InputWatermark    string              `json:"input_watermark"`
    InputRawMemoryIDs []string            `json:"input_raw_memory_ids"`
    Entries           []ConsolidatedEntry `json:"entries"`
    Summary           string              `json:"summary"`
    CreatedAt         time.Time           `json:"created_at"`
    Model             string              `json:"model"`
    PromptVersion     int                 `json:"prompt_version"`
}

type ConsolidatedEntry struct {
    Key             string     `json:"key"`
    Kind            MemoryKind `json:"kind"`
    Content         string     `json:"content"`
    SourceMemoryIDs []string   `json:"source_memory_ids"`
    Confidence      float64    `json:"confidence"`
    FirstSeenAt     time.Time  `json:"first_seen_at"`
    LastSeenAt      time.Time  `json:"last_seen_at"`
    UsageCount      int        `json:"usage_count"`
    LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
    Status          string     `json:"status"`
}
```

`Status` 只允许 `active`、`superseded`、`expired`、`rejected`。删除通过新快照改变状态完成，不就地修改旧快照。

### 5.4 Manifest

`manifest.json` 是唯一的生效指针：

```json
{
  "format_version": 1,
  "active_snapshot_version": 12,
  "input_watermark": "raw-000042",
  "last_extraction_scan_at": "2026-07-26T15:10:00+08:00",
  "last_consolidation_at": "2026-07-26T15:12:00+08:00"
}
```

快照、`MEMORY.md` 和 `memory_summary.md` 全部写成功后，最后原子替换 Manifest。崩溃最多留下未激活文件，不会出现指针已经推进但正文丢失。

## 6. 项目知识加载

### 6.1 文件发现

从 Workspace 根目录向当前活跃文件目录逐级加载：

1. `AGENTS.md`：项目结构、构建方式、工作流和领域知识。
2. `RULES.md`：必须遵循的编码和提交规则。

同名文件按“根目录先加载、深层目录后覆盖”的顺序合并。自动记忆不得覆盖显式规则。

### 6.2 `@include` 语法

```text
@include docs/architecture.md
@include ./rules/go.md
```

约束：

- 只允许相对路径，解析后的真实路径必须位于 Workspace 内。
- 单文件最大 256 KiB，合并后最大 1 MiB。
- 最大深度为 8，最多加载 64 个文件。
- 用规范化绝对路径集合检测循环；检测到循环时返回包含完整引用链的错误。
- 目标不存在、越界或循环时，该知识层加载失败并报告；不能静默忽略必需规则。
- Markdown 代码块内的 `@include` 视为普通文本。

## 7. Phase 1：会话记忆抽取

### 7.1 触发时机

抽取不是“每轮 Loop 结束立刻运行”，而是在以下条件同时满足时进入后台队列：

1. Session 非临时会话，且启用 `memory.generate`。
2. 至少有一个完整 Turn。
3. Session 在 `min_session_idle` 时间内无新消息，或用户显式关闭 Session。
4. Transcript Hash 与最近成功抽取版本不同。
5. Session 未标记为 `polluted` 或 `disabled`。

默认 `min_session_idle = 30m`。这样避免活跃对话被反复提取，也允许异常退出后的会话在下次启动时补处理。

### 7.2 状态机与租约

每个 `jobs/extraction/<session-id>.json` 状态为：

```text
pending -> running -> succeeded
                   -> succeeded_no_output
                   -> failed -> pending
```

`running` 记录 `owner_id`、`lease_expires_at` 和随机 `ownership_token`。只有持有相同 Token 的 Worker 可以提交结果。租约过期后其他进程可接管；失败使用指数退避：1 分钟、5 分钟、30 分钟、1 小时封顶。

第一版并发上限为 2，避免后台任务抢占主对话的模型配额。每次启动最多扫描 100 个 Session、认领 8 个任务。

### 7.3 输入过滤

抽取器读取完整 Transcript，但只向模型发送记忆相关内容：

- 保留用户消息、最终 Assistant 回复、工具名称、工具参数摘要和短结果预览。
- 丢弃思维链、流式增量、终端 ANSI 控制符和重复工具进度。
- 工具 Artifact 仅在结论需要证据时按需读取，读取前校验 SHA-256。
- 输入超过模型预算时按完整 Turn 从旧到新截断，并保留最近 Turn；绝不拆开工具调用和对应结果。
- 对匹配凭据模式的内容先脱敏，再发送给抽取模型。

### 7.4 结构化输出与校验

模型必须返回符合 JSON Schema 的 `RawMemory` 内容。提交前执行：

1. Kind 白名单校验。
2. 每条记忆至少包含一条有效 Evidence，Evidence 的 MessageID 必须存在于 Transcript。
3. `Confidence` 限制在 `[0,1]`。
4. 用户偏好和纠正只有在用户消息提供证据时才允许生成。
5. 项目事实若只来自 Assistant 推断，直接拒绝。
6. 凭据扫描命中的条目丢弃，并记录不含原文的安全计数。
7. 空输出记为 `succeeded_no_output`，不重试。

成功结果以追加方式写入 `raw_memories.jsonl`，随后原子更新 `sources/<session-id>.json` 和任务状态。重复提交通过 `(session_id, transcript_hash, prompt_version)` 幂等键消除。

## 8. Phase 2：全局整合

### 8.1 触发条件

以下任一条件满足时尝试整合：

- Phase 1 新增有效 RawMemory。
- 已生效记忆到期。
- 用户删除 Session 或执行记忆重建。
- Prompt 版本升级，需要重新解释原始记忆。

整合失败不影响现有 Snapshot；下一次启动或退避到期后重试。

### 8.2 全局互斥

`leases/consolidation.lock` 使用原子 `O_CREATE|O_EXCL` 创建，内容包含 Owner、Token、PID、Host 和过期时间。持有者每 30 秒续租，租约默认 10 分钟。

接管过期租约时先校验过期时间，再把旧锁原子移动到 `quarantine/`，然后创建新锁。PID 只用于诊断，不能单独作为进程存活判断依据。

### 8.3 输入选择

第一版最多选择 200 条有效 RawMemory，规则依次为：

1. 排除 `disabled`、`polluted`、已删除 Session 的来源。
2. 排除超过 `retention_days` 且从未被使用的条目。
3. 优先选择最近使用、使用次数高、最近生成的条目。
4. 稳定排序使用 `key`、`session_id`、`raw_memory_id`，保证相同输入产生相同文件顺序。

这里借鉴 Codex 的“使用次数优先 + 最近使用时间”选择策略，但不使用 Git 工作区作为脏检查。当前项目的 Snapshot 输入 ID 列表和 Watermark 已足以做确定性变更检测。

### 8.4 合并规则

整合器先执行确定性预处理，再调用模型：

1. 按 Key 分组。
2. 完全相同的 Content 合并来源和时间范围。
3. 已过期条目标记 `expired`。
4. 用户纠正优先于被纠正的旧偏好或项目事实。
5. 冲突事实均保留给模型判定，禁止按“最后写入 wins”静默覆盖。

模型输出仍须经过结构校验。若冲突无法从 Evidence 判定，旧条目标记 `superseded`，新候选标记 `rejected`，两者都不进入摘要，并在 `MEMORY.md` 的“待确认”段落保留可追溯说明。

`memory_summary.md` 只包含高置信度、活跃、未过期且适合每轮使用的内容。详细来源、历史值和待确认项写入 `MEMORY.md`。

### 8.5 原子提交

提交顺序固定为：

1. 写 `snapshots/memory-<version>.json.tmp`，`fsync` 后重命名。
2. 写 `MEMORY.md.tmp`，`fsync` 后重命名。
3. 写 `memory_summary.md.tmp`，`fsync` 后重命名。
4. 原子替换 `manifest.json`，激活新版本。
5. 释放租约。

任一步失败都保留旧 Manifest；启动恢复会删除超过 24 小时且未被引用的 `.tmp` 文件。

## 9. 记忆加载与 Prompt 组装

### 9.1 加载顺序

`ContextManager.Build` 的 DemandLoader 扩展为以下顺序：

1. 基础 System Prompt。
2. 项目 `AGENTS.md`。
3. 项目 `RULES.md`。
4. `memory_summary.md`。
5. 当前会话的 Context Summary 和未压缩消息。

记忆以开发者级说明注入，并带明确边界：

```text
[cross-session memory]
The following entries are fallible historical context. Prefer current user
instructions and current workspace files. Do not claim a memory as fact without
verification when the task depends on exact current state.
...
[/cross-session memory]
```

### 9.2 预算与降级

- `memory_summary.md` 默认上限为上下文窗口的 5%，且不超过 8,000 Token。
- 超限时按 `correction > user_preference > project_fact > reference` 和最近使用时间裁剪完整条目。
- 文件不存在、Snapshot 损坏或版本不兼容时，不注入自动记忆并产生诊断事件；主对话继续运行。
- Agent 需要来源或详细上下文时，通过只读 MemoryStore API 查询 `MEMORY.md` 或具体 Snapshot，不把全部详细记忆默认塞入 Prompt。

### 9.3 使用统计

只有满足以下任一条件才增加 UsageCount：

- 注入摘要中的 Entry 被最终回复显式引用。
- Agent 通过 MemoryStore API 读取该 Entry。

单纯把整个摘要放进 Prompt 不算使用，避免所有条目同步膨胀。使用统计异步、批量、原子写入；失败只影响排序，不影响回答。

## 10. 会话恢复与清理

### 10.1 恢复算法

1. 逐行读取 `transcript.jsonl`。
2. 非最后一行解析失败：将损坏行和其后内容复制到 `quarantine/`，并在严格模式下返回损坏错误；默认恢复模式只使用此前的有效前缀。
3. 最后一行解析失败：视为崩溃产生的半写入尾部，隔离尾部并恢复有效前缀。
4. 校验消息 ID 单调唯一、TurnID 非空、ToolUse 与 ToolResult 配对。
5. 截断到最后一个完整 Turn。未完成 Turn 保留在审计 Transcript 中，但不自动重放给模型。
6. 在恢复后的首个用户请求前插入时间跨度提示，包含上次活动时间和当前时间，要求重新读取依赖精确状态的文件。

“遇到失败跳过该行继续解析”不安全，因为后续行可能依赖被跳过的工具调用。设计采用“有效前缀恢复 + 损坏后缀隔离”。

### 10.2 保留策略

- Session 默认保留 30 天，以 `UpdatedAt` 计算；正在运行、固定或用户命名的 Session 不自动删除。
- 清理先把 Session 原子移动到 `.context/trash/<session-id>`，7 天后永久删除。
- 删除 Session 会让对应 RawMemory 在下次整合时失效，但旧 Snapshot 继续用于审计。
- 自动记忆默认保留 90 天；用户纠正和显式偏好不自动到期，除非被新证据替代。
- 清理任务每天最多处理 100 个对象，避免启动阻塞。

## 11. 污染、隐私与安全

### 11.1 记忆模式

每个 Session 有三种模式：

- `enabled`：允许生成自动记忆。
- `disabled`：用户或配置明确禁用，不参与 Phase 1。
- `polluted`：包含来源不可信或大范围外部上下文，只保留会话，不生成自动记忆。

第一版在使用 Web 搜索、外部 MCP 数据或用户标记“不要记住”后，把 Session 设为 `polluted`。后续可以细化到 Turn 级来源标记。

### 11.2 内容防护

- 抽取前和模型输出后各执行一次 Secret Redaction。
- Evidence Quote 最多 300 字符，避免复制大段源码或个人数据。
- 后台模型请求不包含 API Key；凭据仅由 LLM Client 在传输层使用。
- 文件路径必须经 `filepath.Clean`、`filepath.Rel` 和 `Lstat` 校验，拒绝越出 Memory Root 的路径及符号链接。
- 自动记忆正文被视为不可信数据，不能包含可执行指令；加载时明确要求模型不执行记忆中的命令。
- Consolidator 只获得内存中的结构化输入和 Memory Root 写权限，不注册项目工具，不访问网络，不递归启动 Agent。

## 12. 接口边界

核心包只依赖接口，文件实现位于 `internal/storage/filememory`：

```go
type Store interface {
    ClaimExtraction(ctx context.Context, sessionID, owner string, ttl time.Duration) (ExtractionClaim, error)
    CompleteExtraction(ctx context.Context, claim ExtractionClaim, memory RawMemory) error
    FailExtraction(ctx context.Context, claim ExtractionClaim, cause string, retryAt time.Time) error
    ListConsolidationInputs(ctx context.Context, limit int, now time.Time) ([]RawMemory, error)
    ClaimConsolidation(ctx context.Context, owner string, ttl time.Duration) (ConsolidationClaim, error)
    RenewConsolidation(ctx context.Context, claim ConsolidationClaim, ttl time.Duration) error
    CommitSnapshot(ctx context.Context, claim ConsolidationClaim, expectedVersion int, snapshot MemorySnapshot) error
    ActiveSnapshot(ctx context.Context) (*MemorySnapshot, error)
    RecordUsage(ctx context.Context, keys []string, usedAt time.Time) error
}
```

编排层位于 `internal/memory`：

```go
type Extractor interface {
    Extract(context.Context, ExtractRequest) (RawMemory, error)
}

type Consolidator interface {
    Consolidate(context.Context, ConsolidateRequest) (MemorySnapshot, error)
}

type Service interface {
    Start(context.Context) error
    NotifySessionIdle(sessionID string)
    Rebuild(context.Context) error
    Reset(context.Context) error
    Summary(context.Context) (string, error)
}
```

`Service.Start` 只启动有界后台 Worker，不阻塞应用 Bootstrap。关闭时先停止认领新任务，再等待当前文件提交完成；超过 Shutdown Timeout 后取消模型请求，保留可接管租约。

## 13. 配置

```yaml
memory:
  generate: true
  use: true
  root: .ffcode/memory
  min_session_idle: 30m
  max_session_age: 30d
  raw_memory_retention: 90d
  max_extractions_per_startup: 8
  extraction_concurrency: 2
  max_consolidation_inputs: 200
  summary_token_limit: 8000
  extract_model: ""       # 空值表示使用 summary.model，再回退主模型
  consolidation_model: ""
  disable_on_external_context: true
```

配置规则：

- `generate` 控制写路径，`use` 控制读路径，二者独立。
- 所有数量必须大于 0；持续时间不得为负。
- Root 必须解析到 Workspace 内。
- 专用模型未配置时优先复用 `summary.model`，再回退主模型。
- 配置热更新只影响新任务；正在运行的 Claim 使用认领时快照。

## 14. 可观测性与用户命令

日志和指标不得包含记忆正文，只记录 ID、数量、耗时、Token 和状态：

- `memory.extraction.jobs{status}`
- `memory.extraction.duration_ms`
- `memory.consolidation.jobs{status}`
- `memory.consolidation.duration_ms`
- `memory.entries{kind,status}`
- `memory.redaction.count`
- `memory.recovery.quarantined_files`

终端命令：

- `/memory status`：显示开关、当前版本、最后成功时间、待处理任务数。
- `/memory inspect`：打开或打印 `MEMORY.md`，不显示已隔离敏感内容。
- `/memory rebuild`：从现存 RawMemory 重建 Snapshot，不重新读取 Transcript。
- `/memory reset`：经用户确认后清空自动记忆、任务和 Snapshot；不删除 Session 或项目知识。
- `/memory disable`：对当前 Session 禁用自动记忆生成。

## 15. 错误处理

| 失败点 | 行为 |
| --- | --- |
| Transcript 追加失败 | 当前 Turn 不得报告成功，向用户返回持久化错误 |
| Phase 1 模型失败 | 记录失败和退避，不影响主会话 |
| Phase 1 输出非法 | 视为不可重试输出错误；Prompt 版本变化后可重试 |
| RawMemory 追加成功但任务状态未更新 | 下次通过幂等键识别并补完成 |
| Consolidation 租约丢失 | 立即停止提交，保留旧 Snapshot |
| Phase 2 模型失败 | 保留旧版本并退避 |
| Snapshot 文件写成功但 Manifest 未切换 | 文件为孤立未生效版本，启动时可清理 |
| Active Snapshot 损坏 | 隔离损坏文件，尝试回退最近可校验版本；无可用版本则禁用记忆注入 |
| 项目知识 include 错误 | 本轮 Build 失败并指出引用链，避免忽略强制规则 |

所有错误用稳定的 Sentinel Error 分类，文件路径和底层错误通过 `%w` 包装。只有会影响当前请求正确性的会话持久化、项目知识和 Prompt 构建错误向上返回；后台记忆错误进入诊断面。

## 16. 测试策略

### 16.1 单元测试

- RawMemory Schema、Kind、Evidence 和 Confidence 校验。
- Key 规范化、确定性去重、冲突和过期规则。
- `@include` 的正常、嵌套、循环、越界、符号链接和大小限制。
- Transcript 尾部半写入、中央损坏、重复 ID、未配对 ToolResult 恢复。
- 租约认领、续租、过期接管、错误 Token 提交。
- Snapshot 乐观锁、原子激活、孤立版本回收。
- Secret Redaction 前后双重检查。
- Prompt 优先级、Token 裁剪和记忆缺失降级。

### 16.2 集成测试

1. 创建 Session，写入多轮对话，重启后恢复相同有效 Transcript。
2. 两个进程同时认领同一 Session，只有一个 Phase 1 结果生效。
3. 多个 Phase 1 并行完成，Phase 2 只产生一个单调递增 Snapshot。
4. 在每个原子提交步骤注入崩溃，重启后只能看到旧完整版本或新完整版本。
5. 删除来源 Session 后重建，相关 Entry 不再注入，但历史 Snapshot 可审计。
6. 外部上下文污染 Session 后，不产生 RawMemory。
7. 模型返回 Prompt Injection、密钥和不存在的 Evidence 时，结果被拒绝。

### 16.3 属性与竞争测试

- 对任意损坏 JSONL，恢复结果必须是原 Transcript 的有效前缀。
- 相同输入集合无论排列顺序如何，确定性预处理结果一致。
- `go test -race ./internal/memory/... ./internal/storage/filememory/...` 无数据竞争。
- 文件存储 Fuzz Test 覆盖 JSONL、Manifest、Snapshot 和租约解析。

## 17. 实施阶段

### 阶段 1：会话可靠性

完善 Transcript 恢复、完整 Turn 校验、时间跨度提示、软删除和 30 天清理。此阶段不引入自动记忆。

### 阶段 2：项目知识

实现 `AGENTS.md`、`RULES.md` 发现和安全 `@include`，接入 DemandLoader，并完成 Token 预算测试。

### 阶段 3：Phase 1

实现文件任务状态、租约、抽取器、结构化校验、脱敏和 RawMemory 幂等写入。默认开启生成，但仍不注入 Prompt。

### 阶段 4：Phase 2 与读取

实现全局整合、Snapshot 原子提交、摘要注入、使用统计和 `/memory` 诊断命令。先通过配置 opt-in 开启 `use`。

### 阶段 5：默认启用与运维

完成崩溃注入、竞争测试、性能基线和升级/回滚验证后，默认开启 `use`。保留 `generate`、`use` 独立关闭和 Reset 能力。

每个阶段都可以独立发布和回滚；不允许在同一提交中同时更换会话事实模型和自动记忆整合模型。

## 18. 验收标准

1. 强制终止进程后，已确认写入的完整 Turn 可以恢复，半写入内容不会进入模型上下文。
2. 同一 Transcript 只生成一个对应 Prompt 版本的 RawMemory。
3. 两个应用进程并发运行 100 次，不出现重复生效的整合版本或 Manifest 回退。
4. 任何自动记忆都能追溯到 Session、Message 和原始 Evidence。
5. 用户当前指令或 Workspace 文件与记忆冲突时，回答使用当前事实并可淘汰旧记忆。
6. 后台模型、磁盘或锁失败不阻塞主对话，也不破坏上一版可用记忆。
7. 自动测试证明 Secret 不会出现在 RawMemory、Snapshot、Markdown 或日志中。
8. 关闭 `memory.use` 后 Prompt 不包含自动记忆；关闭 `memory.generate` 后不创建新抽取任务。

## 19. 与 Codex 参考实现的取舍

保留的设计：

- 按会话抽取、全局整合的两阶段流水线。
- Phase 1 有界并发、任务租约、失败退避和无输出成功状态。
- Phase 2 全局互斥、输入 Watermark、稳定排序和旧版本保留。
- 自动记忆读写开关分离，外部上下文可污染会话。
- 整合 Worker 无网络、无项目工具、不可递归委派。

有意简化的部分：

- 不引入 SQLite 状态库，先以原子文件和租约满足单机多进程一致性。
- 不用 Git 仓库作为 Memory Workspace 基线；Snapshot 输入 ID 和 Manifest 提供变更检测与回滚。
- 不自动生成 Skill 或扩展资源目录。
- 不在应用启动时强制完成整个 Pipeline；后台任务仅消费有界工作量。

当任务量、检索规模或多进程竞争使文件扫描成为可测瓶颈时，可以新增 SQLite Store 实现。业务层接口、状态机和文件导出格式保持不变，因此迁移不要求重写抽取与整合逻辑。
