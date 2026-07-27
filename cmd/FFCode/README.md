# FFCode 命令

本目录是 FFCode CLI 的构建入口。

- `main.go`：传递命令行参数和标准输入输出，并使用应用返回值退出进程。
- `version`：可在构建时通过 `-ldflags` 注入。

该入口必须保持轻量，具体初始化统一放在 `internal/app`。

## 机器输出

使用 `--output-format jsonl` 启用结构化 Agent 事件：

```bash
printf 'inspect the repository\n' | FFCode --cwd /path/to/project --output-format jsonl
```

JSONL 模式下每个非空输入行启动一个 Turn，stdout 不包含欢迎文本、提示符、Markdown 渲染或 ANSI 控制字符。协议版本 1 不解释 slash command；以 `/` 开头的输入会作为普通用户请求处理。
