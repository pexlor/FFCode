# FFCode SWE-bench Lite 110-case 评测报告

## 1. 报告说明

本报告记录 FFCode 代码 Agent 在 2026-07-28 执行的一次 SWE-bench Lite 子集评测。Agent 共处理 110 个 case，其中 Astropy 6 个、Django 104 个。该批次不是 SWE-bench Lite 全量 300-case，因此 `81/110` 不能直接作为官方全量榜单成绩使用。

报告依据以下运行产物生成：

- Agent 结果：`/tmp/mycode-swe-full/agent-results.jsonl`
- Agent 日志：`/tmp/mycode-swe-full/agent-logs/`
- Agent 补丁：`/tmp/mycode-swe-full/patches/`
- Evaluator 结果：`/tmp/mycode-swe-full/evaluator-watch/evaluation-results.jsonl`
- Evaluator 日志：`/tmp/mycode-swe-full/evaluator-watch/logs/`
- SWE-bench Harness 报告：`/tmp/swebench-harness.ImQnZk/logs/run_evaluation/`

统计口径固定在 Evaluator 完成 `110/110` 后。`resolved` 表示目标失败测试转为通过，且要求保持通过的测试没有回归；`unresolved` 表示补丁成功应用，但未满足全部测试要求。

## 2. 核心结论

| 指标 | 结果 |
|---|---:|
| 总 case 数 | 110 |
| Resolved | 81 |
| Unresolved | 26 |
| Evaluator error | 1 |
| 空补丁跳过 | 2 |
| 总体成功率 | 73.6% (`81/110`) |
| 有效评测通过率 | 75.7% (`81/107`) |
| 非空补丁通过率 | 75.0% (`81/108`) |
| 补丁产出率 | 98.2% (`108/110`) |

主要结论如下：

1. FFCode 在该 110-case 子集上解决了 81 个问题，总体成功率为 73.6%。排除 1 个基础设施错误和 2 个空补丁后，有效评测通过率为 75.7%。
2. 26 个 unresolved 补丁全部成功应用，说明主要瓶颈不是补丁格式、Git 操作或 Evaluator，而是补丁语义不完整。
3. 失败集中在需求边界覆盖、兼容行为理解、修复层级选择，以及共享状态和协议不变量。
4. Agent 的 6 个 timeout 中有 5 个补丁被 Evaluator 判定为 resolved。超时状态不能直接等同于修复失败，但暴露出 Agent 无法及时收尾的问题。
5. 两个空补丁具有不同原因：一个在探索阶段超时，另一个已经定位根因，但因迟迟未进入实现而主动以空补丁结束。
6. 1 个 evaluator error 是 Docker Hub 鉴权请求超时，属于基础设施问题，不能用于判断 Agent 修复质量。

## 3. Agent 执行统计

| Agent 状态 | 数量 | 占比 | Evaluator 结果 |
|---|---:|---:|---|
| `completed` | 104 | 94.5% | 76 resolved、26 unresolved、1 evaluator error、1 空补丁 |
| `timeout` | 6 | 5.5% | 5 resolved、1 空补丁 |

Agent 运行时间统计：

| 指标 | 结果 |
|---|---:|
| 批次起止时间 | 17:11:00～19:28:36 CST |
| 批次墙钟时间 | 2 小时 17 分 35 秒 |
| 单 case 平均时间 | 358.84 秒 |
| 单 case 中位数 | 276.28 秒 |
| 单 case P90 | 638.61 秒 |
| Agent 输出总量 | 69,096,680 字节 |
| Agent 平均输出量 | 628,152 字节/case |

超时 case 明细：

| Case | 补丁字节 | Evaluator 结果 | 结论 |
|---|---:|---|---|
| `django__django-12589` | 2455 | resolved | 补丁有效，但 Agent 未及时结束 |
| `django__django-12708` | 2015 | resolved | 补丁有效，但 Agent 未及时结束 |
| `django__django-12747` | 1543 | resolved | 补丁有效，但 Agent 未及时结束 |
| `django__django-12856` | 2373 | resolved | 补丁有效，但 Agent 未及时结束 |
| `django__django-12908` | 968 | resolved | 补丁有效，但 Agent 未及时结束 |
| `django__django-12915` | 0 | skipped | 只进行源码探索，未进入修改阶段即超时 |

## 4. Evaluator 统计

| Evaluator 状态 | 数量 | 平均评测时间 |
|---|---:|---:|
| `resolved` | 81 | 313.25 秒 |
| `unresolved` | 26 | 322.98 秒 |
| `evaluator_error` | 1 | 无有效结果 |
| `skipped_empty_patch` | 2 | 未运行 |

其他运行特征：

- 107 个实际完成评测的 case 累计消耗 33,770.48 秒，串行等价约 9 小时 22 分 50 秒。
- Evaluator 使用 5 并发运行。
- 18 个 case 至少重试 1 次，其中 5 个重试到第 3 次。
- 26 个 unresolved 的补丁均 `patch_successfully_applied=true`。
- 23 个 unresolved 仅有 `FAIL_TO_PASS` 失败。
- `django__django-14667` 和 `django__django-15819` 同时存在目标测试失败和原有测试回归。
- `django__django-13660` 的目标测试全部通过，但 2 个 `PASS_TO_PASS` 测试回归。

## 5. 仓库维度

| 仓库 | Case 数 | Resolved | Unresolved | 其他 | 总体成功率 |
|---|---:|---:|---:|---:|---:|
| Astropy | 6 | 3 | 3 | 0 | 50.0% |
| Django | 104 | 78 | 23 | 3 | 75.0% |

Astropy 样本只有 6 个，统计波动较大，不适合单独推导稳定成功率。Django 是本批次的主体，其中有效评测通过率为 `78/101 = 77.2%`。

## 6. Unresolved 根因分析

### 6.1 失败类型汇总

| 根因类别 | 数量 | 占 unresolved 比例 | 典型表现 |
|---|---:|---:|---|
| 边界条件或对称路径遗漏 | 6 | 23.1% | 只修写不修读、只处理命令大小写、遗漏非法值和混合空数组 |
| 需求语义、兼容策略或修复层级错误 | 12 | 46.2% | 修错入口、把 warning 改成 error、修改与需求相反的行为 |
| 状态、算法或语言协议不变量遗漏 | 8 | 30.8% | clone 共享状态、排序不稳定、`__eq__` 与 `__hash__` 不一致 |

### 6.2 逐 case 根因

| Case | 失败现象 | 根本原因 | 改进方向 |
|---|---|---|---|
| `astropy__astropy-14182` | `dtype` header 被当成数据，浮点转换失败 | 只实现多 header 的写入路径，读取仍使用固定 `start_line=3` | 对读写格式强制做 round-trip 验证，并检查双向入口 |
| `astropy__astropy-14365` | 小写 `no` 数据行无法识别 | 只让 `READ SERR/TERR` 命令大小写不敏感，遗漏 `_new_re`、`_data_re` 和 `NO` 值解析 | 从需求生成完整 token 变体矩阵，而不是只覆盖一个示例 |
| `astropy__astropy-7746` | `([], [1])` 在 broadcast 前失败 | 只处理整体空二维数组，未处理多轴输入中任一轴为空的情况 | 在进入广播和底层函数前检查每个输入轴 |
| `django__django-11019` | 17 个 Media 顺序、去重和 warning 测试失败 | 自行实现的拓扑排序缺少稳定排序和完整冲突处理，输出顺序与警告格式均破坏契约 | 优先复用项目已有稳定拓扑排序工具，并运行全部相关 Media 测试 |
| `django__django-11283` | 已存在目标权限时没有审计警告 | Agent 删除目标权限后迁移，改变了数据保留策略；需求是捕获完整性冲突并提示人工审计 | 涉及迁移和数据冲突时先明确保留、覆盖、删除策略 |
| `django__django-11564` | 直接读取 `STATIC_URL/MEDIA_URL` 时没有 SCRIPT_NAME 前缀 | 修复放在 template static tag，层级过低，其他调用入口完全未覆盖 | 公共配置语义应在 settings 层修复，并搜索全部直接访问入口 |
| `django__django-11848` | mock 后 `current_year` 变成 `MagicMock` | 使用 `datetime.now()`，而既有 UTC 语义和测试接口使用 `datetime.utcnow()` | 修改时间逻辑时保持时区语义和既有 mock 边界 |
| `django__django-11905` | 非布尔 `isnull` 直接抛错，测试流程中断 | 当前 Django 版本要求先发弃用 warning，Agent提前实施下一主版本的硬错误行为 | 从版本和 deprecation 类确认兼容阶段，不直接实现最终行为 |
| `django__django-12308` | 非法 JSON key 触发 `TypeError` | 只覆盖合法 JSON 序列化，没有在非法 JSON 时回退到普通值显示 | 序列化展示逻辑必须覆盖合法、非法、空值和非字符串 key |
| `django__django-13158` | 对 union queryset 调用 `none()` 后原 queryset 也变空 | `combined_queries` 没有在 clone 时深拷贝，`set_empty()` 原地修改共享子查询 | 修改 Query clone 或组合查询时验证原对象不可变性 |
| `django__django-13220` | `ValidationError` 变成 unhashable | 添加 `__eq__()` 后未实现配套 `__hash__()`，违反 Python 数据模型契约 | 修改语言协议方法时检查 equality、hash、ordering 等配套约束 |
| `django__django-13265` | CreateModel、Index、Constraint 和 together 操作顺序错误 | 只移动了一个生成步骤，遗漏新建模型和多类依赖的完整操作顺序 | 对迁移操作建立依赖图，覆盖 create 与 alter 两类状态 |
| `django__django-13321` | 多个 session backend 未记录损坏数据 warning | 异常捕获放在 `_legacy_decode()`，实际 `BadSignature` 分流发生在外层 `decode()` | 沿调用链反向追踪异常，在真正的控制流边界处理 |
| `django__django-13448` | `MIGRATE=False` 时没有调用 migrate/sync 应用 | 把配置理解成完全跳过 migrate；真实语义是禁用 migration module 后仍执行同步 | 配置开关必须验证最终业务语义，不能只按名称推断 |
| `django__django-13660` | 目标函数作用域修复，但 `__name__ in globals()` 为 False | 使用 `exec(code, {})`，缺少 shell 模块 globals；目标通过但引入 2 个回归 | 修复动态执行时检查 globals/locals、内建变量和旧测试 |
| `django__django-13768` | 日志包含函数 repr 和内存地址，格式不稳定 | 使用 `%s` 输出 receiver 对象，而契约要求 `receiver.__qualname__` 和明确 `exc_info` | 日志属于外部契约，应验证完整消息和异常信息，而非只检查包含关键字 |
| `django__django-14155` | ResolverMatch repr 的引号和 partial 表示均错误 | 为修 repr 解包并改写了 partial 本身，混淆了展示语义与调用语义；同时漏掉字符串 `repr` | 修复 `repr()` 时保持对象状态不变，逐字段使用稳定表示 |
| `django__django-14411` | `id_for_label()` 返回空串，期望 `None` | Agent 自建测试固定了错误返回值，没有核对 Widget 的精确 API 契约 | 对 `None`、空字符串和缺失属性进行严格区分 |
| `django__django-14667` | defer/only 链式调用延迟字段数量错误，并破坏旧用例 | 只调整一个早退条件，没有理解 `deferred_loading` 两种模式及状态转换 | 为状态机式 API 枚举连续调用序列，并验证每一步状态 |
| `django__django-15202` | `split_url.hostname` 为 `None` 时调用 `len()` | 提前解析 URL 后默认 hostname 一定存在，遗漏缺 host 的非法输入 | 解析器输出的可空字段必须进入边界矩阵 |
| `django__django-15252` | 空 migration plan 仍创建 `django_migrations` 表 | 修复放在 `MigrationRecorder` 路由判断，真实决策点在 Executor/测试数据库创建流程 | 追踪谁决定是否调用 `ensure_schema()`，在源头而非被调用者修复 |
| `django__django-15320` | 创建 Subquery 后原 queryset 的 `subquery` 状态被改为 True | 构造函数直接持有并修改传入 Query，没有先 clone | 包装查询对象前先确认所有权，验证输入对象不被修改 |
| `django__django-15400` | `SimpleLazyObject + 1` 仍抛 `TypeError` | 实现了 `__radd__`，但目标测试需要正向 `__add__` | 根据操作数位置验证正向和反向运算协议 |
| `django__django-15695` | 反向 RenameIndex 执行了 2 条 SQL，期望 0 条 | Agent 把原本要求保持 no-op 的反向行为改成真实 rename，方向与需求相反 | 修改既有测试预期前必须证明行为契约确实发生变化 |
| `django__django-15819` | related_name 不符合 Django 命名规则，并引入旧测试回归 | 简单使用字段名作为 `related_name`，没有生成稳定且唯一的反向关系名 | 对命名冲突使用项目既有命名规则，并验证保留关键字场景 |
| `django__django-15996` | IntFlag 序列化顺序为 `B | A`，期望 `A | B` | 依赖私有分解函数的返回顺序，并在自建测试中接受了反序结果 | 序列化输出必须显式排序，保证确定性和可重复生成 |

## 7. 非 unresolved 失败

### 7.1 Evaluator error：`django__django-12113`

Agent 生成了 1104 字节补丁，但 Evaluator 连续尝试 3 次均无法拉取对应 Docker 镜像。根因是访问 Docker Hub 鉴权服务超时：

```text
failed to fetch anonymous token
Get "https://auth.docker.io/token?...": context deadline exceeded
```

这是基础设施失败，不是测试失败。该 case 的真实修复结果未知，不应计入有效评测分母。改进措施是提前预拉取全部 instance image、使用镜像代理或认证账号，并将镜像拉取错误与测试错误分开重试。

### 7.2 空补丁：`django__django-12915`

Agent 在约 20 分钟运行中只执行了 16 次读取和搜索调用。日志表明它已经定位到 ASGI static files handler 缺少异步响应路径，但没有进入编辑阶段，最终 timeout 且补丁为 0。

根因是探索阶段没有时间上限，也没有“根因已明确后必须实现”的阶段门禁。

### 7.3 空补丁：`django__django-14997`

Agent 执行了 67 次工具调用，准确定位到 SQLite table remake 后 expression index 被错误加上表名前缀的问题，也完成了最小复现。但它发现共享 `Expressions` 逻辑的既有测试与局部修复目标存在冲突后，继续阅读和推演，最终选择不修改任何文件并正常结束。

根因是分析已经充分，却缺少从“已确认根因”切换到“选择最小实现并验证”的收敛机制。这是典型的 analysis paralysis，而不是理解能力不足。

## 8. Agent 的系统性问题

### 8.1 自建测试容易自证实现

多个失败 case 中，Agent 新增或修改的测试与自己的实现完全一致，却与官方契约相反。例如：

- `django__django-14411` 把空字符串写成期望值，官方要求 `None`。
- `django__django-15695` 修改旧测试，期待反向 rename；官方要求保持 no-op。
- `django__django-15819` 把字段名直接写成 related_name 期望值，忽略 Django 的既有命名规则。
- `django__django-15996` 接受 `B | A` 的反序序列化结果。

因此，“Agent 新增测试通过”只能说明代码符合 Agent 自己的假设，不能单独作为完成证据。

### 8.2 局部补丁多，跨入口检查不足

`astropy__astropy-14182`、`django__django-11564` 和 `django__django-13321` 都属于只修局部入口，没有沿数据流找到真正的公共边界。Agent 需要在修改前明确列出读写路径、直接和间接调用、异常产生与捕获位置。

### 8.3 对兼容契约敏感度不足

warning 与 error、`None` 与空字符串、稳定日志文本、序列化顺序、clone 后原对象不变，都属于隐藏测试常覆盖的精确契约。当前提示词主要要求“运行 focused checks”，没有强制 Agent 检查这些相邻行为。

### 8.4 收尾状态与补丁价值脱节

6 个 timeout 中有 5 个 resolved，说明 Agent 已经形成有效补丁后仍继续工作。另一方面，`django__django-14997` 已完成高质量根因分析，却没有形成补丁。当前状态机同时存在“该停不停”和“该写不写”两类问题。

## 9. 优化建议

### P0：结构化质量门禁

为每个修复增加以下强制产物：

1. `RepairContract`：目标行为、入口、边界输入、兼容行为和状态风险。
2. `ImplementationEvidence`：修改位置为什么是根因层，而不是症状层。
3. `VerificationMatrix`：最小复现、至少 3 个相邻反例、相关模块原有测试。
4. `CounterexampleReview`：补丁最可能失败的 3 个场景，以及是否改变异常、warning、日志、顺序或对象状态。

如果缺少关键证据，Agent 不能声称完成；如果根因已明确且存在可行最小补丁，Agent 不能继续无限探索。

### P0：收尾和实现转换门禁

- 根因明确且连续两轮没有新证据时，强制进入 Implement。
- 已有非空补丁且相关测试通过后，禁止再次扩大搜索范围。
- 时间使用达到 75% 时进入 Finalize，只允许最小验证、保存补丁和输出结论。
- timeout 前保存最近补丁，避免有效修复被状态标记掩盖。

### P1：反自证测试规则

- 优先新增测试，不修改既有测试期望。
- 修改旧测试期望时，必须引用问题描述、版本兼容策略或既有相邻测试作为证据。
- 测试必须包含至少一个不按当前实现路径构造的反例。
- 对序列化、repr、日志和 warning 做精确字符串验证。

### P1：高风险补丁选择性 Review

对以下类型自动触发一次独立 Reviewer：

- clone、缓存和共享状态；
- 排序、拓扑关系和迁移依赖；
- 序列化、repr、日志等稳定输出；
- 异常捕获和兼容 warning；
- 公共 API、settings 和多入口功能。

Reviewer 只负责寻找反例和回归风险，不重复实现，以把平均额外开销控制在 20% 以内。

### P1：Evaluator 基础设施稳定性

- 在正式评测前预拉取 110 个 instance image。
- 配置 Docker Hub 登录或镜像代理。
- 镜像拉取重试与测试重试使用独立预算。
- 对 `evaluator_error` 保留待重跑队列，不能直接结束整个统计周期。

## 10. 下一轮验收目标

建议继续使用同一 110-case 集合作为回归基线：

| 指标 | 当前 | 下一轮目标 |
|---|---:|---:|
| 总体成功率 | 73.6% | ≥ 80.0% |
| 有效评测通过率 | 75.7% | ≥ 82.0% |
| Unresolved | 26 | ≤ 18 |
| PASS_TO_PASS 回归 case | 3 | 0 |
| 空补丁率 | 1.8% | < 1.0% |
| Agent timeout 率 | 5.5% | < 3.0% |
| Evaluator error | 1 | 0 |
| 平均耗时增幅 | 基线 | ≤ 20% |

优先复跑本次 26 个 unresolved、2 个空补丁和 1 个 evaluator error。该 29-case 困难集能更快验证质量门禁是否有效；达到目标后，再在完整 SWE-bench Lite 300-case 上做最终评测。

## 附录 A：110-case 完整明细

说明：Evaluator 时间为单 case harness 耗时，不包含 Agent 修复时间。空补丁和 evaluator error 没有有效评测时间。

| Case | Evaluator 状态 | Agent 状态 | 补丁字节 | Evaluator 秒 | 尝试次数 |
|---|---|---|---:|---:|---:|
| `astropy__astropy-12907` | resolved | completed | 1401 | 149.4 | 1 |
| `astropy__astropy-14182` | unresolved | completed | 1735 | 144.84 | 1 |
| `astropy__astropy-14365` | unresolved | completed | 1409 | 101.1 | 1 |
| `astropy__astropy-14995` | resolved | completed | 1599 | 110.75 | 1 |
| `astropy__astropy-6938` | resolved | completed | 1586 | 44.02 | 1 |
| `astropy__astropy-7746` | unresolved | completed | 2159 | 135.42 | 2 |
| `django__django-10914` | resolved | completed | 3626 | 49.67 | 1 |
| `django__django-10924` | resolved | completed | 1710 | 46.75 | 1 |
| `django__django-11001` | resolved | completed | 1847 | 47.11 | 1 |
| `django__django-11019` | unresolved | completed | 5025 | 184.86 | 1 |
| `django__django-11039` | resolved | completed | 2047 | 48.24 | 1 |
| `django__django-11049` | resolved | completed | 1126 | 46.79 | 1 |
| `django__django-11099` | resolved | completed | 1976 | 47.36 | 1 |
| `django__django-11133` | resolved | completed | 1248 | 47.79 | 1 |
| `django__django-11179` | resolved | completed | 1108 | 50.3 | 1 |
| `django__django-11283` | unresolved | completed | 2666 | 47.27 | 1 |
| `django__django-11422` | resolved | completed | 1810 | 47.49 | 1 |
| `django__django-11564` | unresolved | completed | 3621 | 49.33 | 1 |
| `django__django-11583` | resolved | completed | 1561 | 47.6 | 1 |
| `django__django-11620` | resolved | completed | 2164 | 49.85 | 1 |
| `django__django-11630` | resolved | completed | 4101 | 179.33 | 1 |
| `django__django-11742` | resolved | completed | 4548 | 48.39 | 1 |
| `django__django-11797` | resolved | completed | 1999 | 180.13 | 2 |
| `django__django-11815` | resolved | completed | 5224 | 91.16 | 3 |
| `django__django-11848` | unresolved | completed | 1823 | 46.89 | 1 |
| `django__django-11905` | unresolved | completed | 1494 | 48.96 | 1 |
| `django__django-11910` | resolved | completed | 2750 | 47.49 | 1 |
| `django__django-11964` | resolved | completed | 1299 | 48.41 | 1 |
| `django__django-11999` | resolved | completed | 2259 | 47.96 | 1 |
| `django__django-12113` | evaluator_error | completed | 1104 | - | 3 |
| `django__django-12125` | resolved | completed | 4783 | 47.42 | 1 |
| `django__django-12184` | resolved | completed | 1393 | 52.6 | 1 |
| `django__django-12284` | resolved | completed | 1954 | 49.39 | 1 |
| `django__django-12286` | resolved | completed | 1467 | 191.17 | 2 |
| `django__django-12308` | unresolved | completed | 1641 | 96.06 | 3 |
| `django__django-12453` | resolved | completed | 2504 | 58.16 | 1 |
| `django__django-12470` | resolved | completed | 2376 | 317.4 | 1 |
| `django__django-12497` | resolved | completed | 2107 | 87.78 | 1 |
| `django__django-12589` | resolved | timeout | 2455 | 150.26 | 1 |
| `django__django-12700` | resolved | completed | 2220 | 88.79 | 1 |
| `django__django-12708` | resolved | timeout | 2015 | 84.25 | 1 |
| `django__django-12747` | resolved | timeout | 1543 | 238.92 | 1 |
| `django__django-12856` | resolved | timeout | 2373 | 64.96 | 1 |
| `django__django-12908` | resolved | timeout | 968 | 64.82 | 1 |
| `django__django-12915` | skipped_empty_patch | timeout | 0 | - | - |
| `django__django-12983` | resolved | completed | 1609 | 67.06 | 1 |
| `django__django-13028` | resolved | completed | 1802 | 71.58 | 1 |
| `django__django-13033` | resolved | completed | 2446 | 315.97 | 1 |
| `django__django-13158` | unresolved | completed | 1221 | 296.51 | 1 |
| `django__django-13220` | unresolved | completed | 4462 | 67.2 | 1 |
| `django__django-13230` | resolved | completed | 2899 | 68.67 | 1 |
| `django__django-13265` | unresolved | completed | 2062 | 293.25 | 1 |
| `django__django-13315` | resolved | completed | 2456 | 308.62 | 1 |
| `django__django-13321` | unresolved | completed | 2009 | 66.79 | 1 |
| `django__django-13401` | resolved | completed | 3103 | 635.18 | 1 |
| `django__django-13447` | resolved | completed | 2958 | 877.14 | 1 |
| `django__django-13448` | unresolved | completed | 1817 | 804.58 | 1 |
| `django__django-13551` | resolved | completed | 2674 | 165.46 | 1 |
| `django__django-13590` | resolved | completed | 1756 | 222.02 | 1 |
| `django__django-13658` | resolved | completed | 2197 | 381.9 | 1 |
| `django__django-13660` | unresolved | completed | 2111 | 326.3 | 1 |
| `django__django-13710` | resolved | completed | 2513 | 746.19 | 3 |
| `django__django-13757` | resolved | completed | 1916 | 559.16 | 2 |
| `django__django-13768` | unresolved | completed | 1990 | 448.27 | 1 |
| `django__django-13925` | resolved | completed | 1359 | 802.34 | 1 |
| `django__django-13933` | resolved | completed | 7410 | 774 | 1 |
| `django__django-13964` | resolved | completed | 2466 | 259.4 | 2 |
| `django__django-14016` | resolved | completed | 1152 | 347.82 | 1 |
| `django__django-14017` | resolved | completed | 2044 | 318.19 | 1 |
| `django__django-14155` | unresolved | completed | 2903 | 249.24 | 1 |
| `django__django-14238` | resolved | completed | 1999 | 293.23 | 1 |
| `django__django-14382` | resolved | completed | 1890 | 536.64 | 1 |
| `django__django-14411` | unresolved | completed | 1118 | 442.34 | 2 |
| `django__django-14534` | resolved | completed | 1516 | 418.76 | 1 |
| `django__django-14580` | resolved | completed | 1111 | 447.69 | 1 |
| `django__django-14608` | resolved | completed | 3196 | 209.72 | 1 |
| `django__django-14667` | unresolved | completed | 1252 | 288.99 | 1 |
| `django__django-14672` | resolved | completed | 1645 | 249.65 | 1 |
| `django__django-14730` | resolved | completed | 3055 | 806.89 | 1 |
| `django__django-14752` | resolved | completed | 2337 | 197.78 | 2 |
| `django__django-14787` | resolved | completed | 1530 | 440.29 | 2 |
| `django__django-14855` | resolved | completed | 2152 | 361.85 | 1 |
| `django__django-14915` | resolved | completed | 1134 | 469.88 | 1 |
| `django__django-14997` | skipped_empty_patch | completed | 0 | - | - |
| `django__django-14999` | resolved | completed | 2099 | 468.78 | 1 |
| `django__django-15061` | resolved | completed | 1571 | 931.33 | 1 |
| `django__django-15202` | unresolved | completed | 2636 | 931.33 | 1 |
| `django__django-15213` | resolved | completed | 1459 | 931.32 | 1 |
| `django__django-15252` | unresolved | completed | 2402 | 255.65 | 1 |
| `django__django-15320` | unresolved | completed | 1403 | 675.68 | 1 |
| `django__django-15347` | resolved | completed | 1478 | 293 | 2 |
| `django__django-15388` | resolved | completed | 1411 | 906.99 | 1 |
| `django__django-15400` | unresolved | completed | 1123 | 652.99 | 1 |
| `django__django-15498` | resolved | completed | 1074 | 715.94 | 1 |
| `django__django-15695` | unresolved | completed | 2743 | 538.73 | 1 |
| `django__django-15738` | resolved | completed | 3624 | 1071.74 | 1 |
| `django__django-15781` | resolved | completed | 1781 | 700.82 | 2 |
| `django__django-15789` | resolved | completed | 2994 | 574.17 | 2 |
| `django__django-15790` | resolved | completed | 1872 | 661.91 | 1 |
| `django__django-15814` | resolved | completed | 2443 | 456.41 | 1 |
| `django__django-15819` | unresolved | completed | 2118 | 539.68 | 1 |
| `django__django-15851` | resolved | completed | 1127 | 172.27 | 1 |
| `django__django-15902` | resolved | completed | 1226 | 447.5 | 1 |
| `django__django-15996` | unresolved | completed | 1571 | 665.23 | 1 |
| `django__django-16041` | resolved | completed | 1666 | 868.92 | 1 |
| `django__django-16046` | resolved | completed | 1043 | 499.69 | 3 |
| `django__django-16139` | resolved | completed | 1453 | 441.72 | 1 |
| `django__django-16255` | resolved | completed | 1262 | 203.44 | 2 |
| `django__django-16379` | resolved | completed | 1288 | 399.01 | 1 |
| `django__django-16527` | resolved | completed | 1723 | 303.04 | 2 |
