# MyCode

MyCode 是一个运行在终端中的代码 Agent。它通过 LLM、内置工具和 MCP 工具读取与修改工作区，并使用权限系统控制高风险操作。

## 目录导航

- [`cmd`](./cmd)：可执行程序入口。
- [`internal`](./internal)：应用装配、核心逻辑和基础设施实现。
- [`docs`](./docs)：架构决策、功能规格和实施计划。
- [`doc`](./doc)：专题技术笔记，后续逐步迁移到 `docs`。

## 开发验证

```bash
go test ./...
go vet ./...
```

详细依赖规则参见 [`docs/architecture/overview.md`](./docs/architecture/overview.md)。
