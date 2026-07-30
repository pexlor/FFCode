# FFCode SWE-bench Lite Agent 行为深度分析

## 1. 分析范围

本文基于 2026-07-28 执行的 110-case SWE-bench Lite 子集，重点分析 Agent 的执行行为，而不只是记录 Evaluator 的最终结果。

分析对象包括：

- 26 个 unresolved case；
- 2 个空补丁 case：django-12915、django-14997；
- 1 个 evaluator_error case：django-12113；
- Agent 原始日志、补丁文件、阶段事件和测试输出。

主要运行产物：

- Agent 日志：/tmp/mycode-swe-full/agent-logs/；
- Agent 补丁：/tmp/mycode-swe-full/patches/；
- Evaluator 结果：/tmp/mycode-swe-full/evaluator-watch/evaluation-results.jsonl。

本次没有修改 FFCode 的实现代码，本文只记录行为分析和后续优化要求。

## 2. 核心判断

当前 Agent 的主要问题不是完全无法定位根因，而是无法稳定地把局部理解转换成完整、兼容、可验证的修复。

失败行为通常遵循以下链路：

    局部搜索
      -> 过早选择一个解释
      -> 修改最近的异常点
      -> 用局部或自建测试验证
      -> 忽略对称路径、版本兼容和旧行为
      -> 过早收尾

其中还存在一个相反方向的问题：有些 case 已经完成根因定位，却因为没有明确的实现门槛而持续分析，最终没有补丁。

## 3. 日志统计发现

### 3.1 阶段事件不能反映真实阶段

26 个 unresolved 日志中，18 个始终显示为 explore，但这些 case 实际已经：

- 修改了源码；
- 新增或修改了测试；
- 执行了测试或直接验证脚本。

原因是阶段转换依赖特定工具类型。Agent 通过 Bash 执行 apply_patch 时，写入行为可能没有触发 implement；Django 的 tests/runtests.py 也不总是被识别为验证调用。

这使得当前质量门禁失效：系统不知道 Agent 已经实现了什么，也无法根据真实证据阻止错误收尾。

### 3.2 工具调用数量与成功率没有明显关系

| 状态 | 平均工具调用 | 中位数 |
|---|---:|---:|
| resolved | 47.9 | 45 |
| unresolved | 50.8 | 48.5 |

失败 case 的调用次数略多，但没有形成可用的质量信号。django-11019 用 89 次调用得到局部测试通过，django-14997 用 67 次调用完成根因定位却没有修改，说明重复搜索和代码质量之间没有直接关系。

### 3.3 验证环境不可用时，Agent 会降低结论标准

11 个 unresolved 日志无法运行完整测试，主要原因包括：

- asgiref 版本不兼容；
- Astropy 扩展模块未构建；
- hypothesis 缺失；
- pytest 版本不匹配。

Agent 通常退化为 py_compile、直接函数调用和 git diff --check。这些检查可以保留，但不能与完整测试等价。当前 Agent 仍经常在低置信度下直接宣布完成。

## 4. 逐 case 行为分析

### 4.1 错误的语义或修复层级

| Case | 日志中的行为 | 暴露的问题 |
|---|---|---|
| django-11283 | 先判断应保留目标权限，最后又切换为删除目标、保留源权限 | 数据保留策略没有证据支撑，决策在收尾前发生反复 |
| django-11564 | 因 template tag 能拿到 request，就把修复放在 template 层 | 选择了最方便的入口，而不是公共语义的源头 |
| django-11905 | 直接给 isnull 加 ValueError | 没检查 deprecation timeline，把未来版本行为提前实施 |
| django-13448 | 把 MIGRATE=False 解释为 migration 和 serialization 都跳过 | 根据配置名推断语义，没有追踪测试数据库创建流程 |
| django-15252 | 在 MigrationRecorder 添加统一路由判断 | 修改了被调用者，真正决策点在 Executor/测试数据库流程 |
| django-15400 | 看到加法缺失后实现 __radd__ | 没区分正向和反向操作数 |
| django-15695 | 读到原测试写着 Reverse is a no-op，仍将其改成真实 rename | 通过修改既有测试消除证据冲突 |
| django-15819 | 以字段名直接生成 related_name | 没有复用 Django 现有的关系命名和关键字校验规则 |

共同模式是“最近的异常点修复”：Agent 能解释当前栈，但没有沿着数据流继续追踪谁决定最终行为。

### 4.2 边界和对称路径遗漏

| Case | 已覆盖的路径 | 日志中遗漏的路径 |
|---|---|---|
| astropy-14182 | 多 header 写入 | 多 header 读取，缺少读写 round-trip |
| astropy-14365 | command 行大小写 | data 行、NO 值及混合大小写解析 |
| astropy-7746 | 整体空数组和部分 API | 多轴输入中任一轴为空、不同调用形式组合 |
| django-12308 | 合法 JSON 和部分非法值 | 非字符串 key、普通值回退、空值和非法 JSON 的完整矩阵 |
| django-14667 | 一次 only().defer() 状态转换 | 连续链式调用和每一步 deferred state |
| django-15202 | 报告中的非法 URL | hostname 为 None、缺少 host 等解析结果 |
| django-15400 | other + lazy | lazy + value 的正向运算协议 |
| django-15996 | 当前 _decompose() 返回顺序 | 声明顺序、稳定序列化和重复生成一致性 |

这些 case 的共同缺陷是测试数量并不少，但没有覆盖输入空间的对称维度。

### 4.3 状态、所有权和语言协议遗漏

| Case | Agent 的实现决策 | 失败原因 |
|---|---|---|
| django-13158 | 在 Query.set_empty() 中递归修改组合查询 | 没验证 clone 后的所有权，修改副本影响原 QuerySet |
| django-13220 | 实现 ValidationError.__eq__() | 没同步检查 __hash__()，违反 Python 数据模型协议 |
| django-13321 | 把 base64 解码纳入 _legacy_decode() 异常块 | 没完整追踪外层 decode() 的异常分流和日志边界 |
| django-13660 | 使用 exec(code, {}) | 目标函数作用域通过，但缺少 shell 模块 globals |
| django-14155 | 在构造函数中拆解 functools.partial | 为修复 repr() 改变了 ResolverMatch 的对象和调用语义 |
| django-15320 | 日志声称设置 cloned query，代码却直接修改输入 query | 自然语言结论与实际 diff 不一致，源对象被污染 |
| django-13768 | 日志只断言包含关键词 | 没验证 receiver 精确表示、logger 名称和 exc_info 契约 |
| django-14411 | id_for_label() 返回空字符串 | 没区分 None 与空字符串 |

这类问题要求 Agent 在修改共享对象、协议方法或稳定输出时，自动生成副作用和配套协议清单。

### 4.4 自建测试改变了问题 Oracle

以下 case 的日志明确显示 Agent 通过改变测试期望来支持自身实现：

- django-14411：把空字符串作为正确的 id_for_label() 返回值；
- django-13448：把 migration 和 serialization 同时跳过写入测试期望；
- django-13660：用显式空 globals 的行为重写 shell 测试；
- django-15695：把反向 rename 的 no-op 测试改成真实 rename；
- django-15819：把字段名作为 related_name 写入测试；
- django-15996：发现 B | A 后修改测试接受该顺序。

修改既有测试期望应视为高风险操作，除非 Agent 能引用题目描述、版本兼容策略或多个独立的既有测试作为证据。

## 5. 空补丁行为

### 5.1 django-12915：没有从分析切换到实现

Agent 在约 20 分钟内执行 16 次读取和搜索调用，已经判断 ASGI static files handler 缺少异步响应路径，但没有编辑任何文件。

只要已经找到具体缺失类或方法、可对照的同步实现、现有测试入口和最小实现方向，就不应允许继续无限 Explore。

### 5.2 django-14997：根因正确，但遇到抽象冲突后停滞

Agent 用 67 次调用完成了最小复现，并定位到：

- SQLite table remake 会延迟创建 expression index；
- Expressions.rename_table_references() 给列添加表名前缀；
- SQLite 不允许 expression index 使用这种限定列名。

它随后发现共享 Expressions 逻辑的既有测试期望保留表名前缀，于是继续分析并以空补丁结束。

正确的收敛方式应是：保持公共逻辑不变，在 SQLite table-remake 路径做局部隔离，并用一个回归测试验证该 backend 特例。

## 6. Evaluator error 的边界

django-12113 的 Agent 日志产生了非空补丁，但 Evaluator 连续 3 次无法从 Docker Hub 获取 token。这是基础设施问题，不应与 Agent 行为失败混合统计。

后续应将镜像拉取失败放入独立重试队列，并区分 Agent 修复失败、测试失败、镜像或容器启动失败。

## 7. 系统性根因

### 7.1 强局部性

Agent 擅长找到异常栈附近代码并写出局部补丁，但不擅长确认公共入口、间接调用和真实决策层。

### 7.2 测试驱动但 Oracle 不可靠

Agent 很愿意写测试，但测试经常由当前实现反推，且修改既有测试的阻力过低。

### 7.3 自然语言总结比代码证据更乐观

多个日志的最终总结声称“已 clone”“已保证稳定”“已保持兼容”，但实际 diff 没有实现这些保证。最终结论必须由 diff、测试命令和反例检查生成。

### 7.4 环境失败时没有降低结论置信度

当完整测试不可运行时，Agent 仍可能以“已修复”收尾，而不是标记为低置信度并触发额外 Reviewer。

## 8. 优化优先级

### P0：结构化行为契约

Explore 阶段必须产出：

- 目标入口和所有间接入口；
- 正常、空值、非法值和边界值；
- 兼容阶段（warning 还是 error）；
- 读写、正向反向和单项组合项；
- 共享对象所有权和不可变性；
- 日志、repr、序列化顺序等稳定输出要求。

### P0：阶段转换改为证据驱动

- 任意实际 workspace diff 都应进入 Implement；
- Bash + apply_patch、重定向和编辑工具统一识别；
- tests/runtests.py 等项目测试命令统一识别为 Verify；
- 有根因但无补丁时禁止 Finalize；
- 有非空补丁且验证完成后限制继续搜索；
- 时间达到 75% 时自动进入收尾模式。

### P0：测试变更门禁

自动检测并拦截：

- 修改既有断言期望；
- 删除旧测试；
- 只修改测试、不修改源码；
- 测试只覆盖当前实现路径；
- 通过改变测试把 no-op 变成有副作用。

### P1：高风险补丁 Reviewer

以下修改自动触发独立反例审查：

- clone、缓存、共享状态；
- __eq__、__hash__、__repr__、算术协议；
- migration、router、schema、executor；
- serialization、日志、warning；
- settings、公共 API、多入口逻辑。

### P1：diff 与总结一致性检查

检查 Agent 的文字结论是否与代码一致，例如：

- 声称 clone，但没有 clone/copy；
- 声称保留原对象，但存在原对象属性赋值；
- 声称兼容 warning，但引入直接 exception；
- 声称稳定排序，但依赖无序或私有函数返回顺序。

## 9. 下一轮验收标准

建议先重跑本次 26 个 unresolved 和 2 个空补丁，再观察以下过程指标：

| 指标 | 当前观察 | 下一轮目标 |
|---|---:|---:|
| Unresolved | 26 | ≤ 18 |
| 空补丁 | 2 | ≤ 1 |
| PASS_TO_PASS 回归 | 3 个 case | 0 |
| 修改既有测试期望 | 多个 | 0 个无证据修改 |
| 根因明确但无补丁 | 1 个明确 case | 0 个 |
| 有效评测通过率 | 75.7% | ≥ 82% |

## 10. 结论

本次日志证明，FFCode 的最大提升空间在 Agent 的决策和收尾流程，而不只是模型、Token 或运行时间。

优先顺序应是：

1. 用结构化契约替代自由形式探索；
2. 用 workspace diff 和测试证据驱动阶段转换；
3. 禁止无证据修改既有测试期望；
4. 对状态、兼容性、稳定输出和公共 API 触发反例审查；
5. 在根因明确后强制实现，在补丁有效后强制收尾。

这组改动预计比单纯增加运行预算更有收益，因为本次失败中既有“分析不够”的 case，也有“分析已经足够但没有正确决策”的 case。
