# protocol

本目录定义 FFCode 的版本化 Agent 事件协议，并将 Agent 事件编码为逐行 JSON（JSONL）。
协议供 `--output-format jsonl` 使用，便于脚本和其他程序消费流式输出。

## 主要职责

- 定义稳定的事件信封：版本号、序列号、会话 ID、Turn ID 和事件数据。
- 将 `internal/agent` 的事件映射为协议事件。
- 保证每个事件独占一行，并为事件分配单调递增的序列号。

本包不读取 stdin、不管理会话、不执行 Agent，也不负责终端渲染。JSONL 输入输出流程由
`internal/ui/jsonl` 负责。

## 协议版本 1

每行都是一个 JSON 对象，结构如下：

```json
{
  "version": 1,
  "sequence": 3,
  "type": "turn_finished",
  "session_id": "session-id",
  "turn_id": "turn-1",
  "data": {
    "status": "completed",
    "stop_reason": "end_turn",
    "provider_reason": "end_turn",
    "usage": {
      "input_tokens": 100,
      "output_tokens": 20,
      "total_tokens": 120,
      "cache_read_tokens": 0,
      "cache_creation_tokens": 0
    }
  }
}
```

信封字段说明：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `version` | number | 协议版本，当前为 `1`。 |
| `sequence` | number | 进程内事件序号，从 `1` 开始递增。 |
| `type` | string | 事件类型。 |
| `session_id` | string | 会话标识。 |
| `turn_id` | string | 当前 Turn 标识，在同一 Turn 内保持不变。 |
| `data` | object | 与事件类型对应的数据。 |

当前事件类型：

- `turn_started`：Turn 开始。
- `run_phase_changed`：运行阶段发生变化。
- `provider_retry`：可恢复的 Provider 请求将在给定延迟后重试。
- `thinking_started`、`thinking_delta`：Thinking 开始及增量内容。
- `text_delta`：模型文本增量。
- `tool_call_started`、`tool_call_delta`、`tool_call_completed`：Tool Call 的开始、参数增量和完成。
- `tool_execution_started`：即将执行工具。
- `tool_result`：工具执行结果；`is_error` 表示工具是否返回错误。
- `turn_finished`：Turn 的唯一终止事件，包含状态、停止原因和 Token Usage。

`turn_finished.data.status` 的取值为 `completed`、`incomplete`、`failed` 或 `cancelled`。
发生错误时，数据中会包含 `error.message`；协议不会暴露 Go 的错误类型或堆栈信息。

## 编码器 API

```go
encoder := protocol.NewEncoder(writer)

encoder.EncodeTurnStarted(sessionID, turnID)
encoder.EncodeAgentEvent(sessionID, turnID, event)
```

`Encoder` 使用互斥锁保护序列号和输出，适合多个 goroutine 并发提交事件。JSON 编码或写入失败
会直接返回错误；不支持的 `agent.AgentEvent` 类型也会返回错误，不会静默丢弃。

## 事件顺序

JSONL Runner 对每个非空输入行按以下顺序处理：

1. 发出 `turn_started`。
2. 编码 Agent 产生的所有事件。
3. 等待并编码 `turn_finished` 后再读取下一行输入。

如果 Agent 事件流在终止事件前关闭，Runner 会生成一个 `status=failed`、
`stop_reason=agent_error` 的终止事件。
