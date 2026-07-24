# builtin

本目录保存 FFCode 自带的 Tool 实现。

当前包括：

- `Bash`：在指定 Workspace 中执行 Shell 命令。
- `ReadFile`、`WriteFile`、`EditFile`：读取、创建和精确修改文件。
- `Grep`：递归搜索文本。
- `Glob`：按模式列出文件。

每个实现只负责参数校验和具体操作。权限判断统一由父级 `tool.Executor` 完成，内置 Tool 不得自行绕过执行入口。
