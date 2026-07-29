# Context 上下文管理设计

## 系统设计目标

Context 模块在每次模型请求前，从完整会话事实构建一个不超过模型窗口的临时 `ContextView`。压缩和淘汰只改变模型视图，永远不删除或重写 transcript。

目标包括：保留系统规则和最近完整 Turn；限制大工具结果占用；增量压缩中间历史；为模型输出、下一轮工具结果和估算误差预留空间；在发送前阻止确定会溢出的请求。

## 架构设计

```text
ConversationStore + Session + Tool Schemas
                   |
                   v
DemandLoader -> ResultOffloader -> StaleResultEvictor -> ConversationCompactor
                                                              |
                                                              v
                                                        BudgetGuard
                                                              |
                                                              v
                                                        ContextView
```

`conversation` 拥有 Session、Message、Turn 和 Transcript 类型；`context` 只编排有损视图。具体文件持久化由 `internal/storage/fileconversation` 实现。

## 详细设计

### 预算模型

`ContextWindow` 是输入与输出总窗口。系统先扣除：

- `ReservedOutput`：配置项 `context.output_reserve`，默认 8192；
- `ReservedToolResults`：窗口的 10%；
- `SafetyMargin`：窗口的 5%。

剩余部分是 `HardInputLimit`，其 90% 是默认软压缩线。工具历史最多占硬输入预算的 25%；单个工具结果上限为硬预算的 5% 且不超过 8000 Token；单批上限为 15% 且不超过 24000 Token。估算器当前使用保守字符估算，并非 Provider tokenizer。

### 四级处理

1. `DemandLoader` 每轮依据活跃路径加载 Workspace 规则，并选择当前需要的工具 Schema。
2. `ResultOffloader` 在单项或批次超限时把完整工具结果写入 `tool-results/`，模型视图只保留预览、Artifact ID 和哈希引用。
3. `StaleResultEvictor` 在工具历史超限时淘汰旧结果，保留当前 Turn 的完整调用关系。
4. `ConversationCompactor` 在达到软阈值且存在足够新增完整 Turn 时生成增量摘要。

每一层的 Token 变化写入 `LayerReport`，最终视图含系统提示、摘要、未覆盖原文、选中的工具 Schema、估算 Token 和绝对预算。

### 增量摘要

摘要通过 `SummarySnapshot` 保存版本、前一版本、覆盖到的 Message/Turn ID、正文、Token 估算和关联 Artifact。提交新摘要后从覆盖游标之后重新读取原文，因此同一历史不会重复进入视图或被反复摘要。

若配置 `summary.model`，优先使用专用摘要模型；否则使用主模型作为 fallback。摘要前后分别触发 `pre_compact` 和 `post_compact` Hook。只有成功提交的新摘要会成为活动版本。

### 持久化布局

```text
<workspace>/.context/sessions/<session-id>/
  manifest.json
  transcript.jsonl
  summaries/
  tool-results/
```

Transcript 是审计事实来源。Artifact 带 SHA-256，读取时校验内容是否损坏或被替换。目录与文件默认分别使用 `0700` 和 `0600` 权限。

### 硬限制

四级策略执行后若 `EstimatedTokens > HardInputLimit`，`Build` 返回 `ErrContextBudgetExceeded`，请求不会发送给 LLM。这样避免依赖 Provider 的 context overflow 错误作为常规控制流。

## 功能测试

测试覆盖预算换算、Workspace 规则加载、工具结果卸载和哈希校验、旧结果淘汰、摘要游标、重复构建不重复持久化、Hook 以及硬预算拒绝。

```bash
go test ./internal/context/... ./internal/storage/fileconversation/...
go test -race ./internal/context/...
```
