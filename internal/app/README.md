# app

`app` 是应用组合根（Composition Root），负责选择具体实现并组装完整运行时。

## 主要职责

- 解析 CLI 参数并返回退出码。
- 解析 Workspace、加载配置和构建系统提示词。
- 创建 LLM Client、Context Manager、Conversation Service 和 Agent。
- 注册内置工具、MCP 工具和权限策略。
- 启动终端 UI，并在退出时释放资源。

## 边界

其他内部包不得导入 `app`。业务规则应放在对应核心包中，不能堆积在启动代码里。
