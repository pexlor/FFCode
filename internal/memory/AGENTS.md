# Memory 模块 Agent 规则

- 保持 `internal/memory` 只依赖领域接口，不依赖具体文件存储实现。
- RawMemory 必须保留 Evidence；没有可验证消息来源的条目不得进入持久化层。
- 修改抽取、整合或脱敏逻辑时，同时补充单元测试和失败路径测试。
