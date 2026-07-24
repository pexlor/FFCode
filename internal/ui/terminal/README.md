# terminal

本目录实现 FFCode 的终端用户界面。

## 主要职责

- 读取 TTY 或管道输入。
- 提供命令补全和输入历史。
- 执行 `/new`、`/resume`、`/thinking` 等 Slash Command。
- 渲染 Markdown、Thinking、Tool 状态和 Token Usage。
- 处理 Ctrl+C 中断当前 Turn。

## 边界

终端层接收 `internal/app` 组装好的 Runtime，不创建 LLM、Store、Context Manager 或权限策略。
