# fileconversation

本目录实现基于本地文件的会话、Transcript、摘要和工具结果存储。

## 磁盘布局

每个 Session 目录包含：

- `manifest.json`：会话元数据和当前摘要游标。
- `transcript.jsonl`：只追加的完整消息事实。
- `summaries/`：已经提交的增量摘要。
- `tool-results/`：卸载的大型工具结果及校验信息。

## 约束

- 保持现有 JSON 和 JSONL 格式向后兼容。
- 摘要和淘汰不能删除原始 Transcript。
- 标识符必须经过校验，防止路径穿越。
- Manifest 和摘要提交必须保持原子性。
