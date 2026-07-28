# FFCode

MyCode 是一个运行在终端里的开源代码 Agent。它把大语言模型（LLM）、文件与 Shell 工具、MCP（Model Context Protocol）以及权限控制组合成一个可审计、可扩展的开发工作流。

它可以先阅读代码和上下文，再调用工具执行检查、修改文件、运行命令，并把会话、检查点和长期记忆持久化下来。

> 当前命令行入口位于 `cmd/FFCode`，构建后的可执行文件建议命名为 `ffcode`。

## 特性

- **原生终端 REPL**：流式输出 Markdown、Thinking、Tool Call 状态和 Token Usage，支持输入历史与 `Ctrl+C` 中断当前 Turn。
- **可靠的 Agent 循环**：并发执行同一轮中的只读工具调用，按调用顺序写回结果，并限制时间、Token、工具调用和 Provider 重试预算。
- **安全的工具执行**：内置 `ReadFile`、`WriteFile`、`EditFile`、`Grep`、`Glob` 和 `Bash`，统一经过 Workspace 路径校验、风险分析与用户确认。
- **模型协议兼容**：支持 Anthropic Messages API 和 OpenAI Chat Completions 兼容 API。
- **MCP 工具扩展**：通过 `.agent/mcp.yaml` 启动 stdio MCP Server，并将发现到的工具接入 Agent。
- **会话与恢复**：持久化会话 transcript 和检查点，支持新建、列出、恢复、重命名与删除会话。
- **跨会话记忆**：后台抽取并整合项目记忆；记忆生成和 Prompt 注入可以分别开关。
- **Skill 工作流**：从项目级、用户级和内置目录按需加载 Markdown SOP，支持 `inline` 与隔离的 `fork` 模式。
- **机器可读输出**：通过 `--output-format jsonl` 输出版本化 Agent 事件，方便脚本和 CI 消费。

## 快速开始

### 环境要求

- Go `1.25.8` 或兼容的 Go 1.25 版本
- 一个支持 Anthropic 或 OpenAI 兼容协议的模型服务
- Unix-like 系统需要可用的 `bash`

### 配置模型

MyCode 默认读取 `~/.mycode/config.yaml`。先创建配置文件：

```bash
mkdir -p ~/.mycode
chmod 700 ~/.mycode
cat > ~/.mycode/config.yaml <<'YAML'
model:
  protocol: openai-compat
  base_url: https://api.openai.com/v1
  api_key: sk-your-api-key
  name: gpt-4o
  max_tokens: 8192

context:
  window: 128000
  output_reserve: 8192

memory:
  generate: true
  use: true
YAML
chmod 600 ~/.mycode/config.yaml
```

使用 Anthropic 时，将 `protocol` 改为 `anthropic`，并把 `name` 换成目标模型名称

### 构建并启动

```bash
git clone <your-fork-url>
cd MyCode
mkdir -p ./bin
go build -o ./bin/ffcode ./cmd/FFCode
./bin/ffcode
```

启动后直接输入任务，例如：

```text
请先梳理这个仓库的模块边界，再指出最值得优先修复的测试缺口。
```

默认 Workspace 是当前目录所属的 Git 根目录；也可以显式指定：

```bash
./bin/ffcode --cwd /path/to/project
```

## 终端命令

在 REPL 中输入 `/help` 查看完整帮助。

| 命令 | 作用 |
| --- | --- |
| `/new [标题]` | 创建并切换到新会话 |
| `/sessions` | 列出最近会话 |
| `/resume <id>` | 恢复会话 |
| `/current` | 查看当前会话信息 |
| `/rename <标题>` | 重命名当前会话 |
| `/delete <id>` | 删除非当前会话（需要确认） |
| `/thinking [off\|minimal\|low\|medium\|high\|xhigh\|status]` | 查看或调整思考强度（`on` 兼容为 `medium`） |
| `/clear` | 清屏但保留上下文 |
| `/exit`、`/quit` | 退出程序 |

## 结构化输出（JSONL）

```bash
printf '%s\n' 'inspect the repository and summarize its risks' \
  | ./bin/ffcode --cwd /path/to/project --output-format jsonl
```

每个非空输入行启动一个 Turn。标准输出只包含协议事件，不包含欢迎文本、提示符、Markdown 渲染或 ANSI 控制字符。事件类型和字段见 [`internal/protocol/README.md`](./internal/protocol/README.md)。

## 配置

完整的模型、上下文和记忆配置结构如下：

```yaml
model:
  protocol: openai-compat # openai-compat 或 anthropic
  base_url: https://api.example.com/v1
  api_key: your-api-key
  name: your-model
  max_tokens: 8192
  enable_thinking: false
  thinking_effort: medium # off/minimal/low/medium/high/xhigh
  thinking_budget: 0       # Qwen/Anthropic 可选 token 预算

summary:
  model: ""
  base_url: ""
  api_key: ""

context:
  window: 128000
  output_reserve: 8192

memory:
  generate: true
  use: true
  root: .ffcode/memory
  min_session_idle: 30m
  extraction_concurrency: 2
  max_sessions_per_run: 100
  summary_token_limit: 8000
  extract_model: ""
  consolidation_model: ""
  disable_on_external_context: true

hooks:
  enabled: false
```

`generate` 和 `use` 相互独立：前者控制后台抽取与整合，后者控制是否将记忆注入 Prompt。未填写的上下文和记忆参数会使用默认值。配置文件权限过宽时，程序会输出警告。更多说明见 [`internal/config/README.md`](./internal/config/README.md)。

### 权限策略

在项目中创建 `.agent/permission.yaml` 可以覆盖默认策略。默认策略以 Workspace 为边界，危险操作遵循拒绝或确认原则：

```yaml
default: deny
workspace:
  root: .
tools:
  readfile:
    permission: allow
  grep:
    permission: allow
  bash:
    permission: confirm
    can_write: true
    can_delete: false
protected_paths:
  - ~/.ssh
```

权限配置属于项目安全边界的一部分，请在评审后再提交。详细规则见 [`internal/permission/README.md`](./internal/permission/README.md)。

### MCP Server

在项目中创建 `.agent/mcp.yaml`，即可加载 stdio MCP Server：

```yaml
mcpServers:
  filesystem:
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/path/to/project"]
    env: {}
```

MCP Server 的进程生命周期由 MyCode 管理；工具名称不能与内置工具冲突。详见 [`internal/mcp/README.md`](./internal/mcp/README.md)。

### Skill

Skill 默认从以下目录发现，优先级为“项目级 > 用户级 > 内置级”：

```text
<workspace>/.agent/skills/
<user-config-dir>/mycode/skills/
<install-root>/skills/
```

项目级 Skill 适合团队共享代码规范、发布流程和排障 SOP。格式与示例见 [`internal/skill/README.md`](./internal/skill/README.md)。

## 数据与安全边界

运行时会在 Workspace 中创建以下目录：

```text
.context/sessions/       # 会话与 transcript
.context/checkpoints/    # Agent 检查点
.ffcode/memory/          # 跨会话记忆（可配置）
```

这些目录可能包含代码上下文、模型输出和用户输入，建议加入项目的 `.gitignore`。权限系统可以降低误操作风险，但不能替代操作系统权限、容器隔离或对不可信 Prompt 的审查。

## 开发

```bash
go test ./...
go vet ./...
mkdir -p ./bin
go build -o ./bin/ffcode ./cmd/FFCode
```

修改并发、文件锁或后台 Worker 相关代码后，还应对对应包运行竞态检测，例如：

```bash
go test -race ./internal/agent/...
go test -race ./internal/memory/...
```

架构说明、ADR、功能规格和实施计划集中在 [`docs/`](./docs)。模块依赖规则见 [`docs/architecture/overview.md`](./docs/architecture/overview.md)。

## 目录结构

```text
cmd/FFCode             CLI 入口
internal/app           应用装配与运行时
internal/agent         Agent 执行循环与预算
internal/tool          工具注册、调度与执行
internal/llm           Anthropic / OpenAI 兼容协议
internal/mcp           MCP Client
internal/permission    权限、风险分析与审计
internal/context       上下文预算与压缩
internal/conversation  会话生命周期
internal/memory        跨会话记忆
internal/skill         Skill 发现与按需加载
internal/ui            终端与 JSONL 交互层
docs                   架构、ADR、规格与计划
```

## 参与贡献

欢迎提交 Issue、改进文档和 Pull Request。建议在提交前：

1. 为行为变化补充测试。
2. 运行 `gofmt`、`go test ./...` 和 `go vet ./...`。
3. 在 PR 描述中说明配置、权限或数据目录方面的影响。

涉及权限、模型协议、持久化或事件协议的改动，请同时更新对应模块文档或 `docs/` 中的架构记录。
