# MyCode 项目 Agent 规则

- 修改 Go 文件后运行 `gofmt`。
- 提交前至少运行 `go test ./...`。
- 涉及并发、文件锁或后台 Worker 的改动，还要运行对应包的 `go test -race`。
- 记忆内容是历史上下文，必须优先核对当前工作区文件和用户指令。
