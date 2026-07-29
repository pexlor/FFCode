# Session 会话管理设计

## 系统设计目标

Session 模块是会话领域事实的唯一所有者，统一管理会话元数据、内存消息、持久化 Transcript 和生命周期命令。它保证中断后的历史只恢复到协议完整边界，并与上下文压缩、长期记忆和 UI 保持解耦。

## 架构设计

```text
terminal/jsonl
     |
conversation.Service
     |-- current Session（当前进程内完整历史）
     |-- Store interface
     |      `-> storage/fileconversation
     |-- MemoryProvider
     `-- Hook Dispatcher
```

`conversation` 定义 `SessionMetadata`、`Session`、`Message`、`StoredMessage` 和 Repository 接口。文件布局、JSONL 追加和原子 Manifest 更新属于 `internal/storage/fileconversation`。

## 详细设计

### 领域模型

`Session` 包含 ID、标题、Workspace、时间、持久化状态、系统提示、长期记忆摘要和 History。内存 `Message` 支持普通文本、Thinking、Tool Use 和 Tool Result。

持久化 `StoredMessage` 额外记录 Message ID、Session ID、Turn ID、迭代号、创建时间、工具元数据和 `open|complete` Turn 状态。工具结果还可处于 `full`、`reference` 或 `dropped` 状态。

### 生命周期

- `New` 创建仅驻留内存的未命名会话，并触发 `session_start`；没有用户消息时不创建磁盘目录。
- 第一条用户消息通过 Hook 后创建 Session，未显式命名时从 Prompt 自动生成最多 40 个字符的标题。
- `List` 按 Workspace 列出会话，并把尚未持久化的当前会话合并到结果中。
- `Resume` 支持完整 ID 或无歧义前缀，恢复历史并触发新的 `session_start` 转换。
- `Rename` 校验标题并限制为 80 个字符。
- `Delete` 不允许删除当前会话。

REPL 对应 `/new`、`/sessions`、`/resume`、`/current`、`/rename` 和 `/delete` 命令。

### Prompt 提交

`user_prompt_submit` Hook 在 Session 创建、自动标题和历史追加之前执行。被拒绝的 Prompt 不落盘；被改写的 Prompt 同时用于持久化和后续模型输入。若存储失败，相同 Prompt 的单次执行缓存会重置，以便安全重试。

### 持久化布局与一致性

```text
<workspace>/.context/sessions/<session-id>/
  manifest.json       # 标题、Workspace、时间、消息数和活动摘要游标
  transcript.jsonl    # 一条 StoredMessage 一行
  summaries/          # 上下文摘要版本
  tool-results/       # 大型工具结果 Artifact
```

Transcript 先追加并 `fsync`，随后原子替换 Manifest。标识符仅允许字母、数字、点、下划线和连字符，明确拒绝 `..` 和不安全路径。目录权限为 `0700`，文件权限为 `0600`。

### 恢复语义

恢复时只接受最后一个完整 Turn 之前的有效前缀，丢弃没有对应结果的残缺工具链。只要存在历史或发生截断，系统会追加上下文边界提示，包含上次活动时间并要求重新读取当前文件，避免把旧 Transcript 当成当前 Workspace 事实。

Session 恢复与 Agent Run 检查点是两个层次：Transcript 记录“发生过什么”，检查点记录“Run 在哪个提交边界被打断”。后者详见 [agent.md](./agent.md)。

### 长期记忆

Session 持有 `MemoryProvider` 引用。ContextManager 每次 Build 前调用 `RefreshLongTermMemory`，把最新已提交摘要写入 `Session.LongTermMemory`。`memory.use` 只控制注入，不影响后台生成。

## 功能测试

测试覆盖新建和延迟持久化、自动标题、列表、ID 前缀恢复、重命名与删除、完整 Turn 截断、Thinking 恢复、长期记忆刷新以及 Session/Prompt Hook。

```bash
go test ./internal/conversation/... ./internal/storage/fileconversation/...
go test -race ./internal/conversation/... ./internal/storage/fileconversation/...
```
