# FFCode Agent 问题分析报告

## 1. 报告概述

- **评估对象：** FFCode 代码 Agent
- **数据集：** SWE-bench Lite
- **样本规模：** 已执行 50 个 case
- **Agent 并发数：** 6
- **单 case 超时：** 1,200 秒
- **数据快照时间：** 2026-07-27 12:28:53 CST
- **Agent 状态文件：** `/tmp/mycode-swe-full/agent-results.jsonl`
- **Agent 日志目录：** `/tmp/mycode-swe-full/agent-logs/`
- **补丁目录：** `/tmp/mycode-swe-full/patches/`
- **Evaluator 状态文件：** `/tmp/mycode-swe-full/evaluator-watch/evaluation-results.jsonl`

本报告分析 FFCode 在前 50 个 SWE-bench Lite case 中暴露的问题。报告重点关注 Agent 的任务完成能力、补丁产出能力、运行稳定性和结果统计准确性。

当前 50 个样本由 5 个 Astropy case 和 45 个 Django case 组成，并非从 300 个 case 中随机抽样。因此，本报告适合诊断工程问题，不适合直接推断完整 SWE-bench Lite 成绩。

## 2. 核心结论

FFCode 已表现出较强的补丁生成能力，但任务收敛和运行控制存在明显问题：

1. **补丁产出率较高。** 50 个 case 中有 44 个生成非空补丁，补丁产出率为 88%。
2. **严格完成率偏低。** 表面上有 34 个 `completed`，但只有 30 个收到正式的 `done: end_turn`，严格完成率为 60%。
3. **超时是主要损耗。** 13 个 case 超时，占 26%；其中 10 个已经生成补丁，说明大量工作在完成或接近完成后没有及时收尾。
4. **状态统计不可靠。** Runner 使用任意 `done:` 子串判断结束，至少造成 5 个 case 被错误截断或错误分类。
5. **模型服务错误不是主要问题。** 3 个 `agent_error` 中只有 1 个是真正的 API 错误，另外 2 个来自 Runner 误判。
6. **空补丁原因明确。** 6 个空补丁分别来自状态误判、API 529、`max_tokens` 和长时间分析未落地。
7. **超时补丁仍有价值。** 已有 1 个 timeout case 被官方 Evaluator 判定为 resolved，不能将 timeout 直接视为修复失败。

## 3. 结果统计

### 3.1 Agent 状态与补丁

| Agent 状态 | Case 数 | 非空补丁 | 空补丁 | 占全部 case |
|---|---:|---:|---:|---:|
| `completed` | 34 | 33 | 1 | 68% |
| `timeout` | 13 | 10 | 3 | 26% |
| `agent_error` | 3 | 1 | 2 | 6% |
| **合计** | **50** | **44** | **6** | **100%** |

主要派生指标：

- 补丁产出率：`44 / 50 = 88%`
- 表面完成率：`34 / 50 = 68%`
- 严格完成率：`30 / 50 = 60%`
- 超时率：`13 / 50 = 26%`
- 空补丁率：`6 / 50 = 12%`
- 真正的上游 API 失败率：`1 / 50 = 2%`

### 3.2 实际终止原因

| 实际终止原因 | Case 数 | Runner 记录状态 | 说明 |
|---|---:|---|---|
| 正式 `done: end_turn` | 30 | `completed` | 严格意义上的正常完成 |
| `done: max_tokens` | 1 | `completed` | 未完成，但被记为完成 |
| 正文包含 `done:` | 3 | `completed` | Runner 在总结文本中提前截断 |
| 正文包含 `done:` | 2 | `agent_error` | Runner 在分析过程中提前截断 |
| Anthropic API 529 | 1 | `agent_error` | 上游服务过载 |
| 达到 1,200 秒期限 | 13 | `timeout` | Agent 未主动结束 |
| **合计** | **50** |  |  |

### 3.3 运行耗时

| 状态 | 平均耗时 | 中位耗时 | 最短 | 最长 |
|---|---:|---:|---:|---:|
| `completed` | 447.3 秒 | 357.3 秒 | 80.4 秒 | 1,133.2 秒 |
| `agent_error` | 294.2 秒 | 319.6 秒 | 80.1 秒 | 483.0 秒 |
| `timeout` | 1,234.6 秒 | 1,234.7 秒 | 1,233.4 秒 | 1,235.5 秒 |

Timeout 比配置的 1,200 秒多约 34 秒，主要来自 Runner 在超时后等待进程退出的 30 秒窗口和清理开销。

### 3.4 Evaluator 临时结果

报告生成时，Evaluator 已处理 22/50 个 case：

| Evaluator 状态 | 数量 |
|---|---:|
| `resolved` | 14 |
| `unresolved` | 5 |
| `skipped_empty_patch` | 2 |
| `evaluator_error` | 1 |

当前端到端解决率下限为 `14 / 50 = 28%`。剩余 28 个 case 尚未评测，因此这不是最终成绩。

## 4. 主要问题

### 4.1 P0：Runner 的完成状态检测存在根本缺陷

Runner 当前使用以下逻辑检测 Agent 是否结束：

```python
if "done:" in text:
    completed_turn = True
    process.stdin.close()
    break
```

该逻辑存在 3 个问题：

1. **任意正文都可能触发。** `are done:`、`should be done:` 和 `what was done:` 都会被视为终止信号。
2. **没有区分停止原因。** `done: max_tokens` 与 `done: end_turn` 都被记录为 `completed`。
3. **只检查当前输出块。** 正式终止标记如果恰好被拆到两个输出块中，可能无法被检测到。

已确认的误判包括：

| Case | 触发文本 | 后果 |
|---|---|---|
| `astropy__astropy-7746` | `arrays of size 1 are done:` | 分析过程中被截断，空补丁 |
| `django__django-12184` | `see what should be done:` | 分析过程中被截断，仅保留 451 B 补丁 |
| `django__django-12453` | `summary of what was done:` | 总结过程中被提前截断 |
| `django__django-12708` | `summary of what was done:` | 总结过程中被提前截断 |
| `django__django-13321` | `summary of what was done:` | 总结过程中被提前截断 |

这使 `completed` 和 `agent_error` 都不能准确反映 Agent 行为，并直接污染后续统计。

### 4.2 P0：Agent 缺少任务收敛机制

13 个 timeout 是当前最大的稳定性问题。日志呈现出一致模式：

- 已经生成补丁后继续扩大测试范围；
- 相关测试通过后仍反复读取源码和 diff；
- 在多个候选方案之间反复推演；
- 遇到环境或测试问题后持续诊断，没有保留当前成果并结束；
- 没有感知剩余 wall-clock 时间，也没有软截止时间。

典型 case：

| 类型 | Case | 超时前状态 |
|---|---|---|
| 测试已通过但未收尾 | `django__django-10924` | 表单测试通过后继续扩展模型测试 |
| 测试已通过但未收尾 | `django__django-11422` | 69 个测试通过，继续重复检查；补丁最终 resolved |
| 测试已通过但未收尾 | `django__django-11564` | 相关测试通过后继续检查环境和更多测试 |
| 测试已通过但未收尾 | `django__django-12856` | Constraint 测试通过且修复验证成功，仍继续添加测试 |
| 测试已通过但未收尾 | `django__django-12915` | 209 个测试通过，继续执行综合测试 |
| 卡在失败测试 | `django__django-11630` | 单独运行成功、完整测试失败，持续调试 Router 状态 |
| 卡在失败测试 | `django__django-12125` | 已有补丁，测试失败后重复过滤和重跑输出 |
| 长时间方案推演 | `django__django-11797` | 持续分析 SQL `GROUP BY`，没有写入补丁 |
| 长时间方案推演 | `django__django-12470` | 持续推演排序方向语义，没有写入补丁 |
| 测试污染排查 | `django__django-12747` | 持续调查删除测试污染，没有写入补丁 |

这 13 个 timeout 中有 10 个非空补丁，说明主要问题不是不会修，而是不会在有限时间内停止。

### 4.3 P0：`max_tokens` 仍会造成假完成和空补丁

当前用户配置仍为：

```yaml
max_tokens: 4096
```

`django__django-11019` 累计输入约 943,448 token、输出约 29,050 token，最终以 `done: max_tokens` 结束。Agent 一直分析媒体文件排序冲突，但没有修改代码。Runner 随后将其记录为 `completed`。

这里有两个相互独立的问题：

- Agent 在长上下文中缺少压缩和决策能力；
- Runner 没有将 `max_tokens` 视为可重试或未完成状态。

### 4.4 P1：瞬时 API 错误缺少完整恢复能力

`django__django-12589` 因 Anthropic API 返回 HTTP 529 `overloaded_error` 而失败。该 case 在约 80 秒时终止，未生成补丁。

这属于瞬时外部错误，不应直接消耗一个 benchmark case。当前系统已针对部分畸形工具 JSON 做重试，但 429、529 和其他可恢复的 5xx 错误仍需要统一的退避重试策略。

### 4.5 P1：状态名称与修复质量耦合错误

当前状态体系容易导致错误结论：

- `completed` 不保证有补丁，也不保证正常结束；
- `timeout` 不代表补丁无效；
- `agent_error` 可能只是 Runner 的字符串误判；
- `evaluator_error` 是基础设施问题，不应计为模型修复失败。

已有直接证据：`django__django-11422` 虽然被标记为 timeout，但官方 Evaluator 判定为 resolved。

Agent 执行状态和补丁质量应拆成两个独立维度：

- **执行维度：** `end_turn`、`max_tokens`、`wall_timeout`、`provider_error`、`runner_error`；
- **产物维度：** `nonempty_patch`、`empty_patch`、`patch_apply_error`；
- **评测维度：** `resolved`、`unresolved`、`evaluator_error`。

### 4.6 P1：超时后的现场不可继续

Runner 在每个 case 结束后删除 worktree，只保留补丁和终端日志。这能够节省磁盘，但带来以下限制：

- 无法从超时位置继续同一个 Agent 会话；
- 无法直接复查未提交的测试文件和临时诊断文件；
- 无法利用已完成的分析进行短时间续跑；
- 只能重新准备仓库并从头执行。

对于已有非空补丁的 timeout case，保存轻量检查点或可恢复 worktree 可以显著降低重跑成本。

### 4.7 P2：当前 50-case 样本不具代表性

本次运行按任务文件顺序处理，前 50 个样本中 Django 占 90%。该结果适合发现 Runner 和 Agent 工作流问题，但不能代表 FFCode 在 SWE-bench Lite 全部仓库上的整体能力。

后续小规模实验应采用固定随机种子或按仓库分层抽样，避免优化只针对 Django 生效。

## 5. 空补丁分析

| Case | Agent 状态 | 根因 |
|---|---|---|
| `astropy__astropy-7746` | `agent_error` | 普通正文 `are done:` 触发 Runner 误判 |
| `django__django-12589` | `agent_error` | Anthropic API 529 过载 |
| `django__django-11019` | `completed` | 长时间分析后 `done: max_tokens`，没有落地修改 |
| `django__django-11797` | `timeout` | 长时间推演 SQL 方案，没有执行修改 |
| `django__django-12470` | `timeout` | 长时间推演排序逻辑，没有执行修改 |
| `django__django-12747` | `timeout` | 卡在测试行为分析，没有保留有效修改 |

空补丁并非单一能力问题，而是由 4 类原因组成：Runner 误判、上游错误、token 限制和 Agent 不收敛。

## 6. 改进建议

### 6.1 P0：修复终止协议

优先采用结构化事件判断 Agent 状态，不再解析终端展示文本。如果短期内只能解析文本，至少应满足：

- 去除 ANSI 控制字符后按完整行匹配；
- 仅接受正式状态行；
- 使用跨输出块缓冲区；
- 记录原始 `stop_reason`；
- 只有 `end_turn` 进入 `completed`；
- `max_tokens` 进入 `incomplete` 或触发续跑。

建议状态示例：

```text
completed:end_turn
incomplete:max_tokens
failed:provider_error
failed:runner_error
timeout:wall_clock
```

### 6.2 P0：增加软截止和强制收尾阶段

建议将 1,200 秒拆为两个阶段：

1. **正常工作阶段（0～900 秒）：** 允许分析、修改和运行相关测试。
2. **收尾阶段（900～1,200 秒）：** 禁止扩展任务范围，要求保留当前补丁、运行最小验证并结束。

收尾阶段的指令应明确要求：

- 不再添加非必要测试；
- 不再进行大范围源码浏览；
- 当前补丁可用时优先保留；
- 测试环境失败时记录原因并结束；
- 输出修改摘要和已运行的测试。

### 6.3 P0：增加循环检测

可根据以下信号识别 Agent 是否陷入循环：

- 多次读取相同文件或相同行区间；
- 连续运行相同命令且结果不变；
- 多次出现 `Let me look`、`Let me check` 等重复规划；
- 已有通过测试后继续扩大验证范围；
- 补丁长时间没有变化。

触发循环检测后，应要求 Agent 在当前方案上做出选择、保留补丁并进入收尾阶段。

### 6.4 P1：统一处理可恢复的模型错误

对以下错误增加指数退避和抖动重试：

- HTTP 429；
- HTTP 529；
- 可恢复的 HTTP 5xx；
- 网络连接重置和读取超时；
- 已识别的工具 JSON 截断。

建议将 case 级状态记录为 `retrying`，只有耗尽重试预算后才进入 `provider_error`。

### 6.5 P1：保存可恢复检查点

对于非空补丁的 timeout case，至少保存：

- 当前 Git diff；
- 最近一次测试命令及结果；
- Agent 的最后决策摘要；
- 修改过的文件列表；
- 可选的压缩 worktree。

重跑时优先从现有补丁继续，而不是从 base commit 重新分析。

### 6.6 P1：重构统计口径

最终报告至少应同时展示：

| 指标 | 计算方式 | 用途 |
|---|---|---|
| 端到端解决率 | `resolved / attempted` | 衡量 Agent 的真实修复能力 |
| 补丁有效率 | `resolved / nonempty_patch` | 衡量已生成补丁的质量 |
| 严格完成率 | `end_turn / attempted` | 衡量任务收敛能力 |
| 补丁产出率 | `nonempty_patch / attempted` | 衡量是否能形成候选修复 |
| 超时率 | `wall_timeout / attempted` | 衡量时间控制能力 |
| 基础设施错误率 | `evaluator_error / attempted` | 隔离评测环境问题 |

`resolved / 非空补丁` 只能作为条件指标，不能替代 SWE-bench 的端到端解决率。

## 7. 建议的修复顺序

1. 修复 `done:` 子串检测，并为 `max_tokens` 建立独立状态。
2. 为 529、429 和可恢复 5xx 增加重试。
3. 增加 900 秒软截止和强制收尾提示。
4. 增加重复工具调用和补丁停滞检测。
5. 保存 timeout case 的可恢复检查点。
6. 重跑 6 个空补丁 case 和 2 个被 `done:` 误截断的 case。
7. 使用分层随机的 50-case 样本进行回归验证。
8. 回归通过后再运行完整 300-case SWE-bench Lite。

## 8. 建议验收指标

完成上述改进后，下一轮 50-case 回归建议关注：

| 指标 | 当前值 | 建议目标 |
|---|---:|---:|
| `done:` 状态误判率 | 至少 10% | 0% |
| 严格完成率 | 60% | >= 80% |
| 超时率 | 26% | <= 10% |
| 空补丁率 | 12% | <= 5% |
| 瞬时 API 错误导致的 case 损失 | 2% | 0% |
| 非空补丁产出率 | 88% | >= 90% |

最终质量目标必须使用 `resolved / attempted` 衡量，并在相同数据集、相同超时、相同网络和相同 Evaluator 配置下比较。

## 9. 总结

FFCode 当前的主要短板不是无法生成修复，而是运行控制和结果分类：

- 44/50 个 case 已生成补丁，说明基础修复能力具备可用性；
- 13 个 timeout 中有 10 个保留了补丁，说明 Agent 经常无法及时停止；
- Runner 的 `done:` 子串检测污染了 10% 以上的状态；
- `max_tokens`、API 529 和评测基础设施错误尚未被正确隔离；
- 当前 evaluator 结果已经证明部分 timeout 补丁可以成功修复问题。

下一阶段应先修复 Runner 状态机和 Agent 收尾机制，再扩大测试规模。否则继续跑 300 个 case 会重复消耗计算时间，并得到含有系统性误差的统计结果。
