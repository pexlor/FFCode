# 产品功能设计

## 产品定位

FFCode 是运行在终端中的本地代码 Agent。用户用自然语言提出开发任务，Agent 读取 Workspace、调用模型和工具、修改代码、运行验证，并把会话、检查点与跨会话记忆保存在本地。

它不是只生成建议的 Chatbot：工具执行、权限确认、运行预算、恢复和机器可读事件协议都是产品核心能力。

## 当前产品形态

当前只有一个 CLI 入口：`cmd/FFCode`。构建后建议命名为 `ffcode`。

```bash
ffcode [--cwd /path/to/project] [--output-format text|jsonl]
ffcode --help
ffcode --version
```

默认 Workspace 是当前目录所属的 Git 根目录；`--cwd` 可以指定起始目录。当前没有独立的 `chat`、`run`、`apply`、`test`、`commit` 或 `index` 子命令，这些名称不能写成已支持接口。

### 交互终端

`text` 是默认模式，提供持续 REPL、Markdown 渲染、Thinking 与工具状态、输入历史和 `Ctrl+C` 取消当前 Turn。

内置命令：

| 命令 | 功能 |
| --- | --- |
| `/new [标题]` | 新建并切换 Session |
| `/sessions` | 列出当前 Workspace 的会话 |
| `/resume <id>` | 按完整 ID 或无歧义前缀恢复 |
| `/current` | 查看当前会话 |
| `/rename <标题>` | 修改当前会话标题 |
| `/delete <id>` | 确认后删除非当前会话 |
| `/thinking ...` | 查看或调整模型思考强度 |
| `/clear` | 清屏但保留上下文 |
| `/help` | 显示帮助 |
| `/exit`、`/quit` | 退出 |

### JSONL 模式

`--output-format jsonl` 面向 Runner 和 CI。stdin 每个非空行启动一个 Turn，stdout 只输出带版本的结构化事件，不混入欢迎文本、Prompt、ANSI 或 Markdown 渲染。终止由明确的 `turn_end` 事件表达，不需要从自然语言中猜测任务是否完成。

## 核心能力

### Agent 运行时

- 模型与工具多轮循环；
- 相邻只读工具并发、写和独占工具串行；
- 时间、输入/输出 Token、工具调用和 Provider 重试预算；
- 429、529、可恢复 5xx 和临时网络错误重试；
- Explore、Implement、Verify、Finalize 阶段；
- 无进展检测、工作区证据和告警式质量门禁；
- 取消、超时检查点和安全恢复；
- 隔离、只读且有独立预算的子 Agent。

### 代码工具与安全

内置 `ReadFile`、`WriteFile`、`EditFile`、`Grep`、`Glob` 和 `Bash`。所有调用经过 Workspace 路径校验、Hook、项目权限策略和必要的终端确认。权限系统降低误操作风险，但不替代操作系统权限、容器隔离或对不可信输入的审查。

### 模型接入

支持 Anthropic Messages API 和 OpenAI Chat Completions 兼容 API，统一流式文本、Thinking、工具调用、Usage 与 Provider 错误。用户可在运行时调整 Thinking effort。

### 会话与上下文

会话保存用户消息、模型文本、Thinking、工具调用和工具结果。恢复只使用完整 Turn，并插入时间边界提示。ContextManager 会卸载大型工具结果、淘汰陈旧结果并增量压缩中间历史，同时保留原始 Transcript。

### 扩展能力

- MCP：从 `<workspace>/.agent/mcp.yaml` 启动 stdio Server 并注册远端工具；
- Skill：从项目、用户和安装目录发现 Markdown SOP，按需加载 Inline Skill；
- Hook：在工具、Session、Prompt、停止、压缩和子 Agent 生命周期运行受信任命令；
- 长期记忆：后台从稳定 Session 抽取并整合本地跨会话记忆。

## 配置模型

### 用户配置

唯一默认用户配置路径是：

```text
~/.ffcode/config.yaml
```

它保存模型、摘要、Context、Memory 和 Hook 启用开关。模型字段中包含 API Key，文件建议设为 `0600`。

### 项目配置

```text
<workspace>/.agent/permission.yaml  # 权限
<workspace>/.agent/mcp.yaml         # MCP
<workspace>/.agent/hooks.yaml       # Hook
<workspace>/.agent/skills/          # Skill
```

项目配置可进入版本控制，但其中的命令、Server 和 Skill 都应按不可信 Workspace 输入审查。

### 运行数据

```text
<workspace>/.context/sessions/      # 会话和 Transcript
<workspace>/.context/checkpoints/   # Run 检查点
<workspace>/.ffcode/memory/         # 长期记忆，路径可覆盖
```

这些内容可能包含源码、用户输入和模型输出，通常应加入 `.gitignore`。

## 模块边界

```text
cmd/FFCode -> app -> ui
                  -> conversation
                  -> agent -> context
                           -> llm
                           -> tool -> permission
                                   -> builtin / MCP / Skill / Subagent
                  -> memory worker
```

`app` 是组合根；核心包不能依赖 UI、具体 Store 或应用启动逻辑。Conversation 拥有事实 Transcript，Context 只构建模型视图，Agent 只编排 Run，Tool 统一授权与执行。

## 已知限制与后续方向

以下是明确未完成的方向，不属于当前产品承诺：

- Active Skill 的 Session 持久化、管理命令和 fork Skill；
- `/memory` 管理命令、记忆清理和后台错误可观测性；
- MCP Resource、Prompt、Sampling 和 cancellation；
- 精确 Provider tokenizer 与更强的上下文缓存利用；
- Windows 等多平台完整验证；
- Agent Team、多用户/云同步和数据库型记忆；
- 独立非交互任务子命令与发布安装流程。

## 验收基线

功能变更至少满足：

```bash
go test ./...
go vet ./...
go build ./cmd/FFCode
```

涉及并发调度、文件锁、后台 Memory Worker 或 Agent 取消路径时，还应对对应包运行 `go test -race`。结构化输出行为由 `internal/protocol` 测试验证，Agent 修复能力通过 `benchmark/swebench-lite-20260726` 持续回归。
