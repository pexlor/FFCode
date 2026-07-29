# LLM Client 设计

## 系统设计目标

LLM 模块将不同厂商协议统一成 Agent 可消费的流式接口，完整保留文本、Thinking、Tool Call、停止原因和 Usage，同时把错误分类为可重试或永久失败。

当前支持：

- Anthropic Messages API；
- OpenAI Chat Completions 兼容 API。

## 架构设计

`LLMClient` 只暴露：

```go
Stream(req *StreamRequest) (<-chan StreamEvent, <-chan error)
```

`StreamRequest` 包含 Context、System Prompt、Conversation Messages 和 Tool Schemas。具体 Client 把统一消息映射到厂商请求，再把 SSE/流式响应转换成统一事件。Client 不执行工具、不修改 Session，也不决定重试次数。

## 详细设计

### 配置

用户配置文件固定为 `~/.ffcode/config.yaml`：

```yaml
model:
  protocol: openai-compat # 或 anthropic
  base_url: https://api.example.com/v1
  api_key: your-api-key
  name: your-model
  max_tokens: 8192
  enable_thinking: false
  thinking_effort: medium
  thinking_budget: 0
```

`protocol`、`base_url`、`api_key` 和 `name` 必填。`thinking_effort` 支持 `off|minimal|low|medium|high|xhigh`；`enable_thinking` 仅用于兼容旧配置。`thinking_budget` 可覆盖按 effort 映射的 Provider Token 预算。

配置文件权限过宽时会警告，建议使用 `chmod 600 ~/.ffcode/config.yaml`。项目目录中的 `.agent/` 配置不能覆盖模型密钥。

### 统一事件

流式事件包括文本增量、Thinking 增量与签名、工具调用开始/参数增量/完成、Usage 和停止原因。Tool Call 完成事件必须包含稳定的 Tool ID、名称和解析后的参数。

Anthropic Thinking 签名会随 Session 历史保留，以满足后续请求的协议校验。工具结果通过原 Tool ID 与调用配对，不能在取消路径生成空 ID。

### Thinking 控制

支持动态能力的 Client 实现 `ThinkingEffortController` 和兼容的 `ThinkingModeController`。终端 `/thinking` 命令可在请求之间调整强度。OpenAI 兼容实现按服务能力发送 `reasoning_effort` 或兼容字段；Anthropic 实现把 effort 映射为预算，并允许显式 `thinking_budget` 覆盖。

### 错误分类

`ProviderError` 保存 Provider、HTTP 状态、错误类型、Retry-After 和底层错误。429、529、部分 5xx 及临时网络错误标记为可重试；认证、权限、无效参数、模型不存在和上下文超限不重试。真正的重试、退避和 attempt 提交由 Agent 负责。

### 摘要和记忆模型

`summary.model` 可配置独立摘要模型；其 `base_url` 为空时继承主模型，但配置独立模型时必须提供 `summary.api_key`。长期记忆抽取和整合可通过模型名覆盖，当前仍复用主 Client 的协议、地址和凭据。

## 功能测试

测试使用本地 HTTP Server 验证请求映射、流事件解析、Thinking 签名与预算、工具参数、Usage 和结构化 Provider 错误，不依赖真实网络。

```bash
go test ./internal/llm/...
```
