# config

除模型、摘要和上下文预算外，配置文件支持跨会话记忆：

```yaml
memory:
  generate: true
  use: true
  root: .ffcode/memory
  min_session_idle: 10m
  scan_interval: 30s
  extraction_concurrency: 2
  max_sessions_per_run: 100
  summary_token_limit: 8000
  extract_model: ""
  consolidation_model: ""
  disable_on_external_context: true
```

`generate` 控制后台抽取/整合，`use` 控制 Prompt 注入，二者独立。未配置时两者均为 `true`；专用模型为空时复用主模型，非空时复用主 Provider 配置创建对应模型 Client。`root` 为相对路径时相对于当前 Workspace。Worker 启动时立即补扫、在 Turn 完成后安排空闲边界检查，并以 `scan_interval` 兜底恢复遗漏任务。

本目录定义并加载用户配置。

## 主要职责

- 定义模型、摘要和上下文配置结构。
- 从 YAML 和默认值生成最终配置。
- 校验必填字段、数值范围和协议名称。

配置包只描述“使用什么配置”，不创建 LLM Client、工具或存储实例；具体装配由 `internal/app` 完成。
