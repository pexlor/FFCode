# terminal

本目录实现 FFCode 的终端用户界面。

## 主要职责

- 读取 TTY 或管道输入。
- 提供命令补全、输入历史和自适应多行输入；`Ctrl+Enter` 换行，`Enter` 发送。
- 执行 `/new`、`/resume`、`/thinking` 等 Slash Command。
- 渲染 Markdown、Thinking、Tool 状态和 Token Usage。
- 在瞬时状态行实时显示单轮 Agent loop 耗时，并在终态保留最终耗时。
- 处理 Ctrl+C 中断当前 Turn。

## 边界

终端层接收 `internal/app` 组装好的 Runtime，不创建 LLM、Store、Context Manager 或权限策略。
