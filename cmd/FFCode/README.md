# FFCode 命令

本目录是 FFCode CLI 的构建入口。

- `main.go`：传递命令行参数和标准输入输出，并使用应用返回值退出进程。
- `version`：可在构建时通过 `-ldflags` 注入。

该入口必须保持轻量，具体初始化统一放在 `internal/app`。
