# Memory 跨会话记忆系统设计

## 系统设计目标

Memory 从已经完成且稳定的历史会话中提取可复用信息，在后续 Session 构建 Context 时注入短摘要。它与会话 Transcript 和上下文压缩摘要相互独立：Transcript 是完整事实，Context Summary 服务当前会话窗口，Auto Memory 服务未来会话。

目标是本地存储、可追溯、可去重、可恢复并且不阻塞当前对话。当前不使用 Redis、MySQL 或向量数据库。

## 架构设计

```text
.context/sessions transcript
        |
        v
Memory Service（每 5 分钟扫描）
  Phase 1: LLMExtractor --并发/按 Session 租约--> RawMemory
  Phase 2: LLMConsolidator --全局租约--> MemorySnapshot
        |
        v
filememory.Store.Summary -> Session -> Context System Prompt
```

项目知识由 `KnowledgeLoader` 从 `AGENTS.md` 和 `RULES.md` 加载；自动记忆由后台两阶段流水线生成。二者不能覆盖当前用户指令或当前文件事实。

## 详细设计

### 配置和路径

用户配置位于 `~/.ffcode/config.yaml`：

```yaml
memory:
  generate: true
  use: true
  root: .ffcode/memory
  min_session_idle: 30m
  extraction_concurrency: 2
  max_sessions_per_run: 100
  summary_token_limit: 8000
  extract_model: ""
  consolidation_model: ""
  disable_on_external_context: true
```

相对 `root` 按 Workspace 解析，默认得到 `<workspace>/.ffcode/memory/`。`generate` 控制后台 Worker，`use` 控制 Prompt 注入，二者互相独立。专用模型名为空时复用主模型名和 Client。

`summary_token_limit` 和 `disable_on_external_context` 已进入配置结构，但当前运行链路尚未消费这两个字段，不能依赖它们限制实际输入。

### Phase 1：会话抽取

Worker 每 5 分钟扫描同一 Workspace 最近最多 `max_sessions_per_run` 个 Session。只有闲置超过 `min_session_idle` 且最后消息为完整 Turn 的 Transcript 才进入抽取。

候选项由 Session ID、消息数版本和 Transcript SHA-256 标识。每个 Session 的提取任务使用带 Owner、随机 Token 和过期时间的租约；相同版本成功后不会重复处理。默认最多并发 2 个抽取任务。

抽取模型只能返回四类条目：`user_preference`、`correction`、`project_fact` 和 `reference`。每条必须有指向真实 Message/Turn 的 Evidence，Confidence 在 `[0,1]`，用户偏好和纠正必须由用户消息支撑，只有 Assistant 推测的项目事实会被拒绝。

### Phase 2：整合

整合任务持有全局文件租约，读取 RawMemory 和前一活动 Snapshot，调用 LLMConsolidator；解析或模型失败时使用 DeterministicConsolidator fallback。提交时校验活动版本，生成递增 Snapshot，并在所有输出就绪后原子切换 Manifest。

条目状态为 `active`、`superseded`、`expired` 或 `rejected`。摘要是默认注入模型的短文本，详细内容保留用于审计。

### 存储布局

```text
<memory-root>/
  manifest.json
  memory_summary.md
  MEMORY.md
  raw_memories.jsonl
  sources/<session-id>.json
  jobs/extraction/<session-id>.json
  leases/consolidation.lock
  snapshots/memory-<version>.json
  quarantine/
```

目录和文件权限分别为 `0700` 和 `0600`。写入采用临时文件、`fsync` 和原子重命名；过期锁可移入 quarantine。Manifest 是活动 Snapshot 的唯一生效指针。

### 项目知识

`KnowledgeLoader` 支持从 Workspace 根到活跃路径加载 `AGENTS.md` 和 `RULES.md`，并展开代码块外的 `@include relative/path`。Include 必须留在 Workspace 内，限制单文件、总大小、深度和数量，并检测循环及符号链接越界。

当前应用系统提示构建会加载 Workspace 规则；KnowledgeLoader 的更完整“按活跃路径分层”能力主要由 Context 和测试使用。

### 当前限制

- 没有 `/memory` 管理命令和 Session 软删除/自动清理。
- 后台 RunOnce 错误当前不会呈现在 UI 中。
- Memory Pipeline 不独立配置协议、Base URL 或 API Key，只能复用主 LLM Client。

## 功能测试

测试覆盖项目知识和 Include 安全、结构化抽取校验、确定性/LLM 整合、租约、版本冲突、后台并发以及 Context 注入。

```bash
go test ./internal/memory/... ./internal/storage/filememory/... ./internal/context/...
go test -race ./internal/memory/... ./internal/storage/filememory/...
```
