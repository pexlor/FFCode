# conversation

本目录是会话领域的权威来源，统一定义 Session、Message、Turn 和持久化 Transcript。

## 主要职责

- 管理会话的新建、恢复、重命名、列出和删除。
- 管理当前会话及自动标题。
- 定义内存消息和持久化消息模型。
- 定义会话存储所需的 Repository 接口。

## 边界

- 不处理 Token 预算和摘要压缩，这些属于 `internal/context`。
- 不读写具体 JSON 文件，文件实现位于 `internal/storage/fileconversation`。
- 不依赖终端 UI 或模型厂商协议。
