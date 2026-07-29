# agent

本目录实现一次用户请求的 Agent 执行循环。

## 执行流程

1. 从 Context Manager 构建受预算约束的模型输入。
2. 调用 LLM；单次 attempt 完整成功后再发布文本、Thinking 和 Tool Call 事件，避免重试泄漏重复内容。
3. 并发执行同一轮中的工具调用，并按原调用顺序写回结果。
4. 持续循环，直到模型返回最终回复或达到迭代上限。

Run 通过时间、累计 Token、工具调用和 Provider 重试预算限制资源消耗。429、529、可恢复 5xx 和临时传输错误使用有上限的指数退避重试。

## 运行阶段与证据

每次 Run 从 `Explore` 开始，并在首个模型请求前记录 Git workspace baseline。阶段由相对 baseline 的变化和实际验证结果驱动：

- workspace 出现本 Run 产生的变化时进入 `Implement`；
- 发生补丁后验证时进入 `Verify`，验证失败也属于验证阶段；
- `Verify` 后再次修改 workspace 时回到 `Implement`；
- 模型请求结束或达到软预算时进入 `Finalize`。

`Finalize` 只表示 Run 即将结束，不表示验证成功。启动前已有的 staged、unstaged 和 untracked 内容不会自动归因给本 Run；只有它们在 Run 内再次变化时才会成为 Run 证据。

Git baseline 或 diff 不可用、超出有界读取限制时，Agent 继续执行并使用显式写工具元数据降级判断，同时产生 `QG007`。证据采集失败不会改变 Turn 状态。

## 告警式质量门禁

正常结束和首次软预算收尾前会评估质量证据：

- `QG001`：源码变化后没有验证；
- `QG002`：最后一次补丁后验证失败；
- `QG003`：只修改测试，没有修改源码；
- `QG004`：删除测试或修改既有测试期望，需要复核；
- `QG005`：只有语法检查、`git diff --check` 等替代验证；
- `QG006`：验证后代码再次变化；
- `QG007`：workspace diff 证据不可用或不完整；
- `QG008`：执行至少 20 个工具后仍为空补丁。

质量告警是 UI 无关的 `QualityWarningEvent`。它们不会进入 Session 历史、触发额外模型调用、阻止完成、改变 `TurnStatus` 或导致非零退出码。

## 边界

- 通过事件与 UI 通信，不直接输出 ANSI 文本。
- 不加载配置、不创建具体模型，也不决定默认工具集合。
- 不处理文件存储细节，依赖注入的上下文和工具能力。
