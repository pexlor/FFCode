# FFCode SWE-bench Lite 29-case 失败集复跑报告

## 1. 测试说明

本报告记录 FFCode 在 2026-07-30 对上一轮 110-case 测试失败集的复跑结果。复跑范围来自 `mycode-swebench-lite-110-case-report-20260728.md`，共 29 个 case：

- 26 个 `unresolved`；
- 2 个空补丁 case；
- 1 个 `evaluator_error`。

这是针对历史失败样本的困难集回归，不是 SWE-bench Lite 全量 300-case 测试，也不是独立抽样。因此，本次 `7/29` 只能用于判断历史失败 case 的恢复情况，不能与官方榜单成绩直接比较。

### 1.1 数据位置

- 运行根目录：`/tmp/mycode-swe-failures-rerun-20260730/`
- Agent 状态：`/tmp/mycode-swe-failures-rerun-20260730/agent-results.jsonl`
- Agent 日志：`/tmp/mycode-swe-failures-rerun-20260730/agent-logs/`
- Agent 补丁：`/tmp/mycode-swe-failures-rerun-20260730/patches/`
- Evaluator 状态：`/tmp/mycode-swe-failures-rerun-20260730/evaluator-watch/evaluation-results.jsonl`
- Evaluator 日志：`/tmp/mycode-swe-failures-rerun-20260730/evaluator-watch/logs/`
- SWE-bench Harness：`/tmp/swebench-harness.ImQnZk`

### 1.2 运行配置

| 配置项 | 值 |
|---|---|
| Agent | FFCode / FFCode |
| 模型标识 | `FFCode-MiniMax-M3-20260730-failures` |
| 源码提交 | `0734e9dbea6dcc6411567522dadb82efd23a554f` |
| 二进制 SHA-256 | `ae6e9767af9a244a3e0f403f4ecb72566625cac431fe65e939a1d60ae26b4502` |
| Agent 并发 | 6 |
| Agent 单 case 超时 | 1200 秒 |
| Evaluator 并发 | 5 |
| Evaluator 单 case 超时 | 1800 秒 |
| Evaluator 最大尝试次数 | 3 |
| 权限系统 | 关闭，`disabled: true`、`default: allow` |
| Agent 开始时间 | 2026-07-30 10:25:54 +08:00 |
| Agent 结束时间 | 2026-07-30 10:48:07 +08:00 |
| Evaluator 结束时间 | 2026-07-30 10:51:39 +08:00 |

为保持和上一轮测试可比，本次继续使用相同 runner 行为：发送 `problem_statement`，不附加数据集中的 `hints_text`。

## 2. 结果摘要

### 2.1 Agent 结果

| 指标 | 结果 |
|---|---:|
| 总 case 数 | 29 |
| `completed` | 29 |
| `timeout` | 0 |
| `agent_error` | 0 |
| 非空补丁 | 29 |
| 空补丁 | 0 |
| Agent 串行等价总耗时 | 7164.26 秒 |
| Agent 平均耗时 | 247.04 秒 |
| Agent 中位耗时 | 235.93 秒 |
| Agent P90 耗时 | 418.52 秒 |
| Agent 实际墙钟时间 | 约 22 分 12 秒 |
| 补丁总大小 | 66,309 bytes |

与上一轮相比，Agent 执行稳定性有明显改善：原失败集中包含 1 个 timeout 空补丁和 1 个 completed 空补丁，本次 29 个 case 全部正常结束并生成非空补丁。

### 2.2 Evaluator 结果

| 指标 | 结果 |
|---|---:|
| 已处理 | 29 |
| `resolved` | 7 |
| `unresolved` | 21 |
| `evaluator_error` | 1 |
| 总体通过率 | 24.1%（7/29） |
| 排除 Evaluator error 后的有效通过率 | 25.0%（7/28） |
| Evaluator error 重试次数 | 3 |

按仓库划分：

| 仓库 | Case 数 | Resolved | Unresolved | Evaluator error | 有效通过率 |
|---|---:|---:|---:|---:|---:|
| Astropy | 3 | 0 | 3 | 0 | 0.0% |
| Django | 26 | 7 | 18 | 1 | 28.0%（7/25） |
| 合计 | 29 | 7 | 21 | 1 | 25.0%（7/28） |

## 3. 与上一轮对比

### 3.1 状态迁移

| 上一轮状态 | 本轮状态 | 数量 |
|---|---|---:|
| `unresolved` | `resolved` | 6 |
| `unresolved` | `unresolved` | 20 |
| `evaluator_error` | `unresolved` | 1 |
| 空补丁 | `resolved` | 1 |
| 空补丁 | `evaluator_error` | 1 |

本次共恢复 7 个 case，其中 6 个来自原 `unresolved`，1 个来自原空补丁。原 Evaluator error `django__django-12113` 本次成功进入测试，但结果为 `unresolved`，说明上一轮未能判断的补丁实际上没有满足测试要求。

### 3.2 新增解决的 case

| Case | 上一轮状态 | 本轮状态 |
|---|---|---|
| `django__django-11283` | unresolved | resolved |
| `django__django-12915` | 空补丁 / timeout | resolved |
| `django__django-13158` | unresolved | resolved |
| `django__django-13448` | unresolved | resolved |
| `django__django-13768` | unresolved | resolved |
| `django__django-14411` | unresolved | resolved |
| `django__django-15320` | unresolved | resolved |

### 3.3 累计覆盖口径

如果保留上一轮已经通过的 81 个 case，并用本次复跑新增的 7 个 resolved 补充结果，则累计覆盖为：

- 总体累计覆盖：`88/110 = 80.0%`；
- 排除当前 1 个 Evaluator error：`88/109 = 80.7%`。

这是两次运行的 best-of-two 累计覆盖，不是同一次运行的 one-shot 成绩，不能作为官方成功率使用。它适合衡量迭代后总共覆盖了多少历史 case。

## 4. 结果解读

1. **运行稳定性改善明显。** 29 个 Agent case 全部 completed，且全部生成非空补丁，说明权限、收尾和补丁提取链路在本轮没有形成损失。
2. **语义修复能力仍是主要瓶颈。** 排除基础设施错误后，困难集通过率为 25.0%；21 个 case 的补丁成功进入 Evaluator，但仍未满足目标测试。
3. **Astropy 问题没有改善。** 3 个 Astropy case 全部再次 unresolved，仍需重点解决 Agent 本地无法运行真实 Astropy 测试、边界矩阵不足和读写对称路径遗漏的问题。
4. **结果具有随机性或版本改进信号，但不能直接归因。** 6 个原 unresolved 本次变为 resolved，说明当前 Agent 对部分问题能够生成正确补丁；但每个 case 只复跑 1 次，没有重复采样，不能区分代码改进、模型采样波动和上下文差异的贡献。
5. **剩余 case 需要使用本轮新日志重新分析。** 本轮补丁与上一轮不完全相同，不能直接把旧报告的失败原因当成本轮结论。应优先分析仍 unresolved 且补丁变化较大的 case。

## 5. Evaluator error

`django__django-14997` 本次由空补丁改善为 3706-byte 非空补丁，但连续 3 次评测均失败。Harness 日志显示，Docker 本地不存在该 instance image，拉取镜像时访问 Docker Hub 鉴权服务超时：

```text
failed to fetch anonymous token:
Get "https://auth.docker.io/token?...": context deadline exceeded
```

Harness 每次约 61 秒后将该实例记为 error，未生成 instance `report.json`。这是基础设施错误，不代表补丁 unresolved。后续应先单独拉取 `swebench/sweb.eval.x86_64.django_1776_django-14997:latest`，成功后只重跑 Evaluator，无需重新运行 Agent。

## 6. 29-case 完整明细

| Case | Agent 状态 | 补丁 bytes | Agent 秒 | Evaluator 状态 | Evaluator 秒 |
|---|---|---:|---:|---|---:|
| `astropy__astropy-14182` | completed | 1780 | 240.32 | unresolved | 154.43 |
| `astropy__astropy-14365` | completed | 1212 | 161.58 | unresolved | 109.01 |
| `astropy__astropy-7746` | completed | 2303 | 284.17 | unresolved | 55.57 |
| `django__django-11019` | completed | 5378 | 463.75 | unresolved | 58.83 |
| `django__django-11283` | completed | 4388 | 385.88 | resolved | 68.21 |
| `django__django-11564` | completed | 4758 | 427.72 | unresolved | 68.53 |
| `django__django-11848` | completed | 2276 | 114.47 | unresolved | 68.24 |
| `django__django-11905` | completed | 1427 | 132.75 | unresolved | 68.21 |
| `django__django-12113` | completed | 1522 | 418.52 | unresolved | 188.96 |
| `django__django-12308` | completed | 1636 | 178.13 | unresolved | 57.95 |
| `django__django-12915` | completed | 906 | 219.89 | resolved | 57.58 |
| `django__django-13158` | completed | 1651 | 240.22 | resolved | 55.07 |
| `django__django-13220` | completed | 3525 | 248.95 | unresolved | 54.70 |
| `django__django-13265` | completed | 4431 | 434.23 | unresolved | 67.93 |
| `django__django-13321` | completed | 1287 | 146.15 | unresolved | 58.86 |
| `django__django-13448` | completed | 3332 | 165.39 | resolved | 67.89 |
| `django__django-13660` | completed | 2355 | 158.44 | unresolved | 67.75 |
| `django__django-13768` | completed | 1932 | 259.07 | resolved | 67.89 |
| `django__django-14155` | completed | 1799 | 265.94 | unresolved | 57.50 |
| `django__django-14411` | completed | 1117 | 130.19 | resolved | 52.41 |
| `django__django-14667` | completed | 1274 | 256.38 | unresolved | 47.63 |
| `django__django-14997` | completed | 3706 | 364.38 | evaluator_error | - |
| `django__django-15202` | completed | 1343 | 286.44 | unresolved | 61.59 |
| `django__django-15252` | completed | 2178 | 235.93 | unresolved | 62.10 |
| `django__django-15320` | completed | 1443 | 203.97 | resolved | 61.59 |
| `django__django-15400` | completed | 1123 | 153.98 | unresolved | 61.54 |
| `django__django-15695` | completed | 1508 | 175.66 | unresolved | 55.22 |
| `django__django-15819` | completed | 3107 | 233.61 | unresolved | 54.93 |
| `django__django-15996` | completed | 1612 | 178.15 | unresolved | 53.35 |

## 7. 后续建议

1. 单独预拉取 `django__django-14997` 的 SWE-bench image，只重跑该 case 的 Evaluator，补齐最终有效分母。
2. 对 21 个 unresolved 使用本轮 Agent 日志和补丁重新做行为分析，优先检查是否重复出现旧错误模式。
3. 把 7 个 resolved case 固定为回归集，后续 Agent 改动必须保持 `7/7`，避免修复能力回退。
4. 修复 runner 的上下文缺失问题后，将 `version` 和 `hints_text` 纳入新的实验组；不要覆盖本报告结果，以便进行 A/B 对比。
5. 为 Astropy case 提供与 Evaluator 一致的可执行测试环境，避免 Agent 在没有真实测试证据时结束任务。
