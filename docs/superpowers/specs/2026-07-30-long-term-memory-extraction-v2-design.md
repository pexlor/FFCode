# 长期记忆提炼 V2 设计

## 背景

原实现依赖固定周期全量扫描：启动后首轮扫描延迟五分钟，错误不可见，提炼输入包含
Thinking 和未预先脱敏的工具数据，整合也会重复读取已经提交的 RawMemory。短生命周期
CLI 因此可能始终不产生记忆，长生命周期进程则可能重复调用整合模型。

V2 将管线调整为“事件通知 + 持久化状态兜底、增量提炼、严格校验、增量整合、确定性
摘要”。当前文件是目标行为；分阶段交付时，尚未实现的管理命令和跨项目全局存储必须
继续保持关闭或只读。

## 目标与非目标

目标：

- 当前 Turn 和模型请求不被记忆任务阻塞。
- 应用启动即可恢复历史到期任务，不依赖首次定时 Tick。
- 只发送上次成功水位线之后的完整 Turn，并在发送模型前过滤和脱敏。
- 所有持久化条目都能回溯到真实 Transcript Evidence。
- RawMemory 只整合一次；无新输入时不创建 Snapshot。
- 注入 Prompt 的摘要只能由已校验 Active Entry 确定性生成。

非目标：

- V2 第一阶段不引入向量数据库。
- 不把 Session 临时进度转换为跨会话记忆。
- 不自动迁移不同 Workspace 根目录下的会话。
- 在独立全局 Store 落地前，不跨 Workspace 传播 `global` 条目。

## 记忆分类和 Scope

支持 `user_preference`、`correction`、`project_fact` 和 `reference`。每条候选必须带
`global`、`workspace` 或 `session` Scope；缺省值按 `workspace` 兼容。

- `global` 只允许用户偏好和纠正，并要求直接用户证据。
- `workspace` 是默认长期记忆，随当前项目存储和召回。
- `session` 仅用于审计和后续提炼上下文，不进入跨会话摘要。

稳定 Key 使用小写路径形式，例如 `workspace/reference/test-command`。同一 RawMemory 内
不允许重复 Key。

## 触发时机

### 提炼触发

| 事件 | 行为 | 默认延迟 |
| --- | --- | ---: |
| 应用启动 | 异步立即扫描历史到期 Session | 0 |
| Turn 完成 | 通知 Worker，并将空闲边界重置 | 10 分钟 |
| 周期兜底 | 扫描因退出、崩溃或通知丢失遗留的 Session | 30 秒 |
| Session 切换/关闭 | 后续阶段提供加速通知 | 30 秒/立即 |
| `/memory extract` | 后续阶段提供显式立即提炼 | 立即 |
| Prompt 版本升级 | 按限速队列重新提炼 | 后台 |

到期 Session 仍必须满足：已持久化、至少一个完整 Turn、Transcript Hash 不同、租约可
领取、失败退避到期、Workspace 精确匹配。应用退出只需保证 Transcript 和任务状态落盘，
不得等待 LLM 请求。

### 整合触发

新增非空 RawMemory、记忆删除/过期、Session 污染或整合规则升级时触发。第一阶段在每次
提炼扫描结尾领取整合租约；Store 只返回尚未提交的 RawMemory，因此无新增输入时只释放
租约，不创建新 Snapshot。后续可增加 30 秒批量 debounce。

## Phase 1：增量提炼

每个 Session 的 Job 保存最近成功 `source_version`。新版本失败时不能推进该水位线；重试
仍从最近成功位置开始。LLM 只接收水位线之后的消息，SourceVersion 和 Transcript Hash
始终描述完整 Transcript，以保持幂等键稳定。

输入在本地依次执行：

1. 删除 Thinking 和流式中间状态。
2. 对用户/Assistant 文本、工具参数和工具结果做凭据扫描与脱敏。
3. 分别限制正文和工具结果大小。
4. 从新到旧按完整 Turn 裁剪整体预算，不拆分工具调用和结果。
5. 保留 MessageID、TurnID 和 Role，供 Evidence 校验。

模型只返回 `categories` 和 `session_summary`。ID、SessionID、Workspace、SourceVersion、
TranscriptHash、时间、模型名和 PromptVersion 全部由运行时覆盖。空 `categories` 写成
`succeeded_no_output`，不创建 RawMemory。

## Evidence 和安全校验

候选提交前必须满足：

- Kind、Scope、Key、Confidence 和 Evidence 结构合法。
- Evidence MessageID 存在且 TurnID 匹配。
- 规范化空白后的 Quote 必须是消息正文或工具结果的真实子串。
- 用户偏好、纠正必须来自用户消息。
- 项目事实不能只来自 Assistant 推断。
- 包含凭据或 `[redacted]` 证据的候选直接丢弃。

单个候选失败不得让未经过校验的自由文本进入活动摘要。

## Phase 2：增量整合

Manifest 记录已经提交的 RawMemory ID。`ListConsolidationInputs` 按生成时间从旧到新返回
未处理输入，并按批次上限截断；Snapshot 成功提交后，Manifest 原子合并本批 ID。旧格式
Manifest 可从活动 Snapshot 的 `input_raw_memory_ids` 建立兼容水位线。

确定性整合先按 Key 合并内容、来源、置信度和时间。LLM 仅用于语义冲突；其结果必须：

- 保留所有活动旧 Key 和本批候选 Key。
- 只引用已存在的 RawMemory ID。
- 使用合法 Kind、Scope、状态和 Confidence。
- 不包含凭据。

校验失败自动回退确定性整合。LLM 返回的 `summary` 和 `detailed` 不直接生效；系统从验证
后的 Entries 重新渲染，`session` Scope 和低于 0.7 置信度的条目不进入摘要。

## 错误、租约和可观测性

提炼失败先原子写入 Job 的错误和重试时间，再把原始错误返回 Worker。后台 Worker 必须把
非取消、非正常租约竞争错误写到诊断输出。默认退避第一阶段维持一小时，后续改为
1 分钟、5 分钟、30 分钟、1 小时的阶梯退避。

Worker 启动扫描、周期扫描和空闲边界扫描在同一个 goroutine 串行执行；单次提炼内部使用
有界并发。Snapshot 提交继续使用全局租约和乐观版本。

## 配置默认值

```yaml
memory:
  generate: true
  use: true
  root: .ffcode/memory
  min_session_idle: 10m
  scan_interval: 30s
  extraction_concurrency: 2
  max_sessions_per_run: 100
  summary_token_limit: 8000
  extract_model: ""
  consolidation_model: ""
```

专用模型名非空时，以相同协议、Base URL、凭据和思考配置创建独立 Client。摘要限制使用
保守 Rune 上限，确保中日韩文本不会明显超过声明的 Token 预算。

## 后续阶段

1. 增加 Session 切换、显式关闭、手动提炼和整合 debounce。
2. 增加 `/memory status|inspect|pending|forget|resolve|rebuild`。
3. 实现 `pending_conflict` 和显式纠正目标。
4. 将常驻偏好与请求相关 Top-K 召回分离。
5. 增加独立的用户全局 Store；完成前 `global` 仍仅在当前 Workspace 生效。
6. 提供旧 Workspace 数据检测和用户确认式迁移。

## 验收标准

- 启动后无需等待 Tick 即可处理已到期 Session。
- Turn 完成通知不阻塞 Agent。
- Thinking 和测试凭据不出现在 LLM 提炼请求中。
- 空候选不写 `raw_memories.jsonl`。
- 虚构 Quote、Assistant-only 偏好/项目事实被拒绝。
- 第二次无变化扫描不增加 Snapshot 版本。
- 专用模型和摘要上限配置生效。
- `go test ./...` 及记忆、文件存储相关 `go test -race` 通过。
