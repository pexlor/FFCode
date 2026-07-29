# Skill 技能系统设计

## 系统设计目标

Skill 是按需加载的 Markdown SOP。系统只在初始 Prompt 中发布目录元数据，模型确认相关后再通过 `load_skill` 加载正文，从而控制 Token 成本并复用项目工作流。

Skill 文本属于 Workspace 输入，不能覆盖系统权限或直接授予工具能力。

## 架构设计

```text
project/user/builtin sources
       -> Registry.Reload（原子 Catalog 快照）
       -> Manager
            |-> CatalogPrompt（名称和描述）
            |-> load_skill Tool
            |-> Instructions（已激活正文）
            `-> AllowedTools（白名单交集）
```

Registry 负责发现、解析、校验和覆盖；Manager 维护当前任务内的 ActiveSkill；Context 构建时注入目录和已激活指令，并过滤模型可见工具。

## 详细设计

### 发现路径和优先级

按下列来源加载，重名时项目级覆盖用户级，用户级覆盖内置级：

1. `<workspace>/.agent/skills/`
2. `<user-config-dir>/ffcode/skills/`，macOS 通常是 `~/Library/Application Support/ffcode/skills/`
3. `<ffcode-executable-dir>/skills/`

用户级目录使用操作系统的 User Config Directory，并不固定等于 `~/.ffcode/skills/`。同一作用域中存在重名 Skill 会使 Reload 失败；完整扫描成功后才原子替换旧 Catalog。

支持根目录中的单个 Markdown 文件，或以 `SKILL.md` 为入口的目录 Skill。目录内其他 Markdown 视为引用资料，不会被重复发现。隐藏目录和符号链接跳过，单个入口最大 256 KiB。

### 文件格式

```markdown
---
name: go-review
description: Review Go changes and report correctness risks.
mode: inline
allowedTools: [ReadFile, Grep, Glob, Bash]
argumentHint: "[package]"
---

Inspect $ARGUMENTS. Use $1 for the first positional argument.
```

`name` 只允许小写字母、数字和连字符，最长 64 个字符；`description` 必填。`mode` 默认为 `inline`。正文支持 `$ARGUMENTS`、`$ARGUMENT`、`$0` 和 `$1...$n` 的简单字符串替换。

### 加载和激活

`load_skill` 本身按写工具调度，因为它会改变后续 Context。每个任务最多同时激活 3 个 Inline Skill；重复加载同名 Skill 会替换旧实例。正文以 `# Active Skills` 区块注入系统提示。

虽然解析器识别 `mode: fork`，当前 `skill.Manager` 会明确返回“fork mode 未实现”。独立子任务由 `delegateTask` 子 Agent 工具提供，尚未与 fork Skill 集成。文档不得把 fork Skill 标为现有能力。

### 工具可见性和权限

Active Skill 声明 `allowedTools` 时，模型可见工具是所有已激活白名单的交集；没有声明白名单的 Skill 不收缩工具集。未知工具在加载时拒绝。

白名单不是安全边界。工具实际执行时仍经过 `ToolsManager` 和 PermissionManager；Skill 无法扩权，也无法绕过 Workspace 路径限制。

### 当前限制

- ActiveSkill 只存在于当前进程和任务中，尚未写入 transcript，也不会随 Session 恢复。
- 尚无 `/skill list|info|reload|active|unload` REPL 管理命令。
- 尚未实现目录内文件引用展开和 fork SkillRunner。

## 功能测试

测试覆盖项目级覆盖、同作用域重复拒绝、参数渲染、最大激活数、工具白名单交集、未知工具以及 `load_skill` 的写访问声明。

```bash
go test ./internal/skill/... ./internal/agent/...
go test -race ./internal/skill/...
```
