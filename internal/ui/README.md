# ui

本目录保存用户交互适配器。

核心 Agent 通过事件发布运行状态，UI 负责读取输入并将事件渲染给用户。当前实现为：

- `terminal`：终端 REPL、Slash Command、输入组件和流式渲染。

未来新增 Web 或桌面 UI 时，应复用 Agent 和 Conversation 能力，不能复制核心执行逻辑。
