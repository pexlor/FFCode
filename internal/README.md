# internal

本目录保存 FFCode 的内部实现。Go 的 `internal` 规则会阻止仓库外部代码直接导入这些包。

## 分层

- 应用装配：`app`。
- 核心能力：`agent`、`conversation`、`context`、`tool`、`permission`。
- 外部适配：`llm`、`mcp`、`storage`、`ui`。
- 支撑能力：`config`、`prompt`、`workspace`。

核心包不得反向依赖 `app`、`ui` 或 `storage`。完整规则参见 [`../docs/architecture/overview.md`](../docs/architecture/overview.md)。
