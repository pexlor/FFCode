# Agent Core 设计文档

本目录描述 FFCode 当前已经落地的 Agent Core。内容以 `internal/` 下的实现和测试为准，用于解释模块目标、边界、运行链路、配置与验证方式，不把尚未实现的设想写成现有能力。

## 文档索引

| 文档 | 对应实现 | 内容 |
| --- | --- | --- |
| [product.md](./product.md) | `cmd/FFCode`、`internal/app`、`internal/ui` | 产品形态、用户能力和数据边界 |
| [agent.md](./agent.md) | `internal/agent`、`internal/subagent` | Agent Loop、预算、阶段、检查点和子 Agent |
| [context.md](./context.md) | `internal/context` | Token 预算、工具结果卸载、淘汰和增量压缩 |
| [session.md](./session.md) | `internal/conversation`、`internal/storage/fileconversation` | 会话领域模型、持久化和恢复 |
| [llm.md](./llm.md) | `internal/llm` | Anthropic 与 OpenAI 兼容流式协议 |
| [tool.md](./tool.md) | `internal/tool`、`internal/tool/builtin`、`internal/permission` | 工具协议、调度、授权和内置工具 |
| [mcp.md](./mcp.md) | `internal/mcp`、`internal/tool/mcp.go` | stdio MCP Client 与工具适配 |
| [skill.md](./skill.md) | `internal/skill` | Skill 发现、覆盖、加载和工具约束 |
| [memory.md](./memory.md) | `internal/memory`、`internal/storage/filememory` | 跨会话记忆流水线 |
| [hook.md](./hook.md) | `internal/hook` | 生命周期 Hook、失败策略和安全边界 |

## 总体调用链

```text
cmd/FFCode
  -> app（解析 Workspace、加载用户配置、组装依赖）
  -> ui/terminal 或 ui/jsonl
  -> conversation.Service（会话生命周期和用户消息）
  -> agent.Agent（循环、预算、阶段、检查点）
       -> context.ContextManager（构建有界模型视图）
       -> llm.LLMClient（流式模型调用）
       -> tool.ToolsManager（Hook、权限、调度、执行）
            -> builtin / MCP / Skill / Subagent
```

核心包不依赖 UI 或 `app`。`internal/app` 是唯一组合根，负责选择具体的模型 Client、文件 Store、工具、权限策略和后台服务。

## 配置与数据路径

| 路径 | 用途 |
| --- | --- |
| `~/.ffcode/config.yaml` | 用户级模型、摘要、上下文、记忆和 Hook 开关 |
| `<workspace>/.agent/permission.yaml` | 项目工具权限策略 |
| `<workspace>/.agent/mcp.yaml` | 项目 MCP Server 配置 |
| `<workspace>/.agent/hooks.yaml` | 项目 Hook 配置（另支持 `.yml` 和 `.ffcode/hooks.yaml`） |
| `<workspace>/.agent/skills/` | 项目级 Skill |
| `<workspace>/.context/sessions/` | 会话 transcript、摘要和工具 Artifact |
| `<workspace>/.context/checkpoints/` | Agent Run 检查点 |
| `<workspace>/.ffcode/memory/` | 跨会话记忆，路径可由用户配置覆盖 |

Workspace 默认解析为当前目录所属的 Git 根目录，也可以通过 `--cwd` 指定。用户配置不从项目目录读取，避免项目文件静默替换模型密钥和全局运行参数。

## 维护约定

- 行为或配置变化必须同步更新对应文档。
- 文档中的“支持”表示已有代码和测试；规划内容必须明确标注。
- 修改 Go 文件后运行 `gofmt`，提交前至少运行 `go test ./...`。
- 涉及并发、文件锁或后台 Worker 时，对相应包补充 `go test -race`。
