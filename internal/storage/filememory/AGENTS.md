# File Memory 存储规则

- 所有 Manifest、租约和 Snapshot 更新必须使用原子写入。
- 文件权限保持最小权限：目录 `0700`，文件 `0600`。
- 修改租约或并发写入逻辑后必须运行 `go test -race ./internal/storage/filememory/...`。
