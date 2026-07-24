# prompt

本目录负责构建稳定的基础系统提示词（System Prompt）。

## 主要职责

- 嵌入 `system_prompt.md`。
- 注入当前时间、运行平台和 Workspace。
- 定义 Agent 的角色、职责、行为准则和安全边界。

## 边界

- 路径级项目规则由 `internal/context` 动态加载。
- Prompt 包不调用 LLM、不执行工具，也不读取会话历史。
- 新增动态信息时应显式传入，避免在不同组件中重复推断 Workspace。
