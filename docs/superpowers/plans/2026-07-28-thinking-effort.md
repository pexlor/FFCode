# 模型思考强度实现计划

> **面向 AI 代理的工作者：** 使用 TDD 逐步实现并验证本计划。

**目标：** 为 OpenAI 兼容和 Anthropic 模型提供可配置、可运行时切换的思考强度。

**架构：** `llm` 定义统一的 effort 值和控制器；各 provider 将其映射到原生请求字段。配置层保存初始值，终端 `/thinking` 命令通过 agent 控制器更新后续请求。

**技术栈：** Go 标准库、现有 OpenAI SDK、YAML 配置和终端命令注册表。

### 任务 1：统一强度模型与配置

**文件：** `internal/llm/client.go`、`internal/config/config.go`、`internal/config/loader_test.go`、`internal/app/bootstrap.go`

- [ ] 先添加合法值、默认值和 `modelParameters` 传递测试并确认失败。
- [ ] 增加 `ThinkingEffort`、`ThinkingBudget` 及统一控制器，保留旧开关兼容性。
- [ ] 校验 `off/minimal/low/medium/high/xhigh`，格式化并运行配置测试。

### 任务 2：provider 请求映射

**文件：** `internal/llm/openai.go`、`internal/llm/anthropic.go` 及对应测试。

- [ ] 先测试 OpenAI `reasoning_effort` 和 Anthropic `thinking` 请求体。
- [ ] 实现强度到请求字段/默认预算的映射，保留 `enable_thinking` 扩展字段。
- [ ] 运行 LLM 测试和竞态测试。

### 任务 3：终端运行时控制

**文件：** `internal/ui/terminal/commands.go` 及命令测试。

- [ ] 先测试 `/thinking low`、`status` 和旧 `/thinking on`。
- [ ] 增加强度控制接口和命令解析，输出当前强度。
- [ ] 运行全量测试并执行 `gofmt`。
