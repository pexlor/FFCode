# llm

本目录封装大语言模型（LLM）通信协议，并向 Agent 提供统一的流式接口。

## 主要职责

- 定义模型请求、Usage 和流式事件。
- 实现 Anthropic 协议。
- 实现 OpenAI Chat Completions 兼容协议。
- 统一文本、Thinking 和 Tool Call 事件。

本包不执行工具、不管理会话，也不决定上下文压缩策略。模型实例由 `internal/app` 根据配置创建。
