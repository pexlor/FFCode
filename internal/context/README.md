# context

本目录负责在每次模型请求前构建受 Token 预算约束的 `ContextView`。

## 处理层级

1. 按当前请求和活跃路径加载项目规则与工具 Schema。
2. 将大型工具结果卸载为 Artifact 引用。
3. 从当前模型视图中淘汰失效的旧工具结果。
4. 达到软阈值时对完整 Turn 进行增量摘要。
5. 发送前执行硬预算检查。

## 数据边界

- Session、Message、Turn 和 Transcript 类型归 `internal/conversation` 所有。
- 文件持久化实现位于 `internal/storage/fileconversation`。
- 上下文处理只改变模型视图，不删除或重写原始 Transcript。

`ContextManager` 是流程编排入口；`budget`、`offloader`、`evictor` 和 `compactor` 分别实现独立策略。
