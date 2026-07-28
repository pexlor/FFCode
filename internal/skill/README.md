# Skill 系统设计

**状态：** Draft  
**目标模块：** `internal/skill`

## 1. 背景与目标

Skill 是写给 Agent 的可复用标准操作流程（SOP）。它把一类重复任务的目标、步骤、约束、可用工具和输出格式固化下来，使 Agent 不必在每个新会话中重新推导工作流。

本模块的目标是：

- 在启动时以很小的上下文成本让模型知道“有哪些 Skill”；
- 仅在需要时加载某个 Skill 的完整内容；
- 支持项目、用户和内置三种作用域，以及确定的覆盖规则；
- 支持将 Skill 指定的工具集限制为当前任务的可见工具；
- 支持内联执行和隔离执行两种模式；
- 让 Skill 的加载、参数、来源和执行结果可追踪、可恢复。

Skill 不是权限系统，也不是新的工具执行通道。Skill 只能缩小模型可见/可调用工具集合；真正的文件和命令权限仍必须经过 `internal/permission`。

### 非目标

第一阶段不处理：远程 Skill 市场、在线安装、Skill 签名、版本依赖解析，以及多个 Skill 的自动编排。它们可以在稳定的本地 Skill 格式和加载生命周期之上扩展。

## 2. 核心概念

| 概念 | 含义 |
| --- | --- |
| Skill | 一个具名 SOP，由元数据和 Markdown 指令组成。 |
| Catalog | 当前工作区内对模型可见的 Skill 摘要列表。 |
| ActiveSkill | 已被加载并在当前任务中生效的 Skill。 |
| Scope | Skill 来源作用域：项目级、用户级或内置级。 |
| Inline | 将渲染后的 Skill 指令加入当前 Agent 上下文继续执行。 |
| Fork | 在独立的子 Agent 上下文中执行，主 Agent 仅接收结果。 |

Skill 名称使用小写字母、数字和连字符，例如 `release-notes`；名称比较时大小写不敏感。名称是覆盖和调用的唯一标识，不以文件路径为标识。

## 3. 存储与覆盖规则

默认发现目录如下：

```text
<workspace>/.agent/skills/            # 项目级
<user-config-dir>/skills/             # 用户级
<install-root>/skills/                # 内置级
```

其中 `<user-config-dir>` 和 `<install-root>` 由运行时配置提供，不能由 Skill 自身指定。每个根目录可同时包含单文件和目录型 Skill：

```text
skills/
├── review.md
├── release-notes/
│   ├── SKILL.md
│   ├── references/
│   │   └── style-guide.md
│   └── scripts/
│       └── collect.sh
└── nested/
    └── migration.md
```

目录型 Skill 的入口固定为 `SKILL.md`；单文件 Skill 使用任意 `.md` 文件名。发现过程递归扫描，但跳过隐藏目录、符号链接和无法读取的文件。目录型入口与单文件的格式相同。

当多个来源出现同名 Skill 时，按以下顺序选择唯一有效版本：

```text
项目级 > 用户级 > 内置级
```

高优先级版本完整覆盖低优先级版本，而不是合并字段或正文。相同作用域内重名是配置错误：保留 Catalog 中该名称的“不可用”诊断项，`load_skill` 拒绝加载，避免依赖扫描顺序。`skill list --all` 可显示被覆盖的候选项和错误原因。

## 4. Skill 文件格式

Skill 文件与标准 `SKILL.md` 约定保持一致：YAML front matter 后接 Markdown 指令正文。最小示例：

```markdown
---
name: release-notes
description: 根据 Git 提交记录生成面向用户的版本说明。
mode: inline
allowedTools:
  - grep
  - readfile
  - bash
---

# 发布说明流程

1. 阅读最近的提交与变更文件。
2. 按“新增、修复、兼容性影响”组织说明。
3. 不确定的用户影响必须明确标注为待确认。
```

支持的元数据：

| 字段 | 必填 | 说明 |
| --- | --- | --- |
| `name` | 是 | 全局名称；必须符合 `[a-z0-9][a-z0-9-]{0,63}`。 |
| `description` | 是 | 用于 Catalog 和模型意图判断的简短说明。 |
| `mode` | 否 | `inline`（默认）或 `fork`。 |
| `allowedTools` | 否 | 可见工具白名单；为空时不额外缩小工具集。 |
| `argumentHint` | 否 | 在 Catalog 中提示 `$ARGUMENTS` 的用途。 |
| `metadata` | 否 | 保留的扩展字段；当前不参与执行决策。 |

正文是给 Agent 的 SOP。它可以引用同一目录下的静态文件；引用路径必须相对于 Skill 根目录解析，且不得越界。Skill 正文不得声明或提升权限。

## 5. 参数渲染

调用形式为：

```text
$release-notes v1.8.0
```

运行时把调用中名称后的原始文本作为 `ARGUMENTS`，并支持：

- `$ARGUMENTS`：全部参数原文；
- `$ARGUMENT`：`$ARGUMENTS` 的兼容别名；
- `$0`：Skill 名称；
- `$1`、`$2` …：按 shell 风格空白分词后的第 N 个参数。

替换只发生在 Skill 正文和 `argumentHint`，不发生在 `name`、`mode`、`allowedTools` 等控制字段。参数仅是文本，不执行 shell 展开、命令替换或模板表达式；未提供的位置参数替换为空字符串。渲染后内容仍作为普通提示词交给模型，不能绕开权限检查。

## 6. 两段式加载

### 6.1 启动阶段：Catalog 注入

会话启动或 Skill 目录重载后，`SkillRegistry` 解析全部元数据，构建稳定排序的 Catalog。上下文层只注入名称、描述、模式和参数提示，例如：

```text
Available skills:
- release-notes [inline]: 根据 Git 提交记录生成面向用户的版本说明。 Args: 版本号
- security-review [fork]: 对变更进行独立的安全审查。
```

不注入正文、引用文件或完整工具 Schema，控制首次请求的 token 成本。Catalog 是模型可见的能力说明，也是 `/skill list` 的数据来源。

### 6.2 按需阶段：`load_skill`

运行时注册内置工具 `load_skill`：

```go
type LoadSkillInput struct {
    Name      string `json:"name"`
    Arguments string `json:"arguments,omitempty"`
}

type LoadSkillOutput struct {
    Name         string
    Mode         Mode
    Instructions string
    AllowedTools []string
    Source       SourceRef
}
```

模型根据 Catalog 判断需要某项 SOP 后调用 `load_skill`。该工具执行以下步骤：

1. 在有效 Catalog 中按规范化名称查找；
2. 校验元数据、读取正文和本地引用；
3. 渲染参数，并校验 `allowedTools` 中的工具均已注册；
4. 创建 `ActiveSkill`，记录来源、内容摘要、参数和加载时间；
5. 返回模式、渲染后的指令及实际生效的工具名单。

加载失败必须以工具错误返回给模型，并包含可行动的原因（不存在、重名、格式错误、引用越界或未知工具）。不得悄悄回退到较低优先级的同名 Skill。

`load_skill` 本身不属于任何 Skill 的 `allowedTools` 限制，以便模型能在任务中继续选择新的 SOP；它也不直接执行 Skill 内容。

## 7. 执行模型

### Inline 模式

`inline` 是默认模式。`load_skill` 成功后，`ActiveSkill` 通过 `ContextManager` 加入后续模型请求的系统/任务上下文；同一轮工具结果之后即可看到其完整 SOP。它持续生效到当前用户任务结束、显式卸载，或会话切换为止。

多个 ActiveSkill 按加载顺序追加。若它们的指令冲突，后加载 Skill 只在 Skill 指令层覆盖先加载 Skill；系统提示词、用户请求和权限策略始终具有更高约束力。为避免无限累积，默认最多同时激活 3 个 Inline Skill，超出时要求先卸载一个。

### Fork 模式

`fork` 适合独立、可交付的子任务，如审查、调研或生成报告。加载后由 `SkillRunner` 创建一个隔离子 Agent：

```text
主 Agent
  └─ load_skill(fork)
       └─ SkillRunner → 子 Agent（Skill SOP + 调用参数 + 必要任务上下文）
                              └─ 汇总结果 → 主 Agent 的工具结果
```

子 Agent 拥有独立消息历史、迭代上限和取消上下文；默认不继承主 Agent 的全部对话，仅传递当前用户任务、渲染后的 Skill 指令、工作区信息以及 Skill 允许的工具。它的最终摘要和必要产物路径作为 `load_skill` 的结果返回主 Agent。子 Agent 的工具调用仍经过同一 `ToolsManager` 和 `PermissionManager`。

Fork 任务必须传播用户取消信号，并设置独立的迭代/Token 预算。第一阶段可限制为串行运行一个 Fork Skill，避免嵌套 Fork 和失控的并发消耗。

## 8. 工具可见性与权限

每次请求模型前，工具集合由以下规则计算：

```text
effective tools = registered tools ∩ active allowedTools（若存在）
```

- `allowedTools` 只影响模型看到的 Schema 和由 Agent 发起的常规调用；
- 没有声明白名单的 ActiveSkill 不改变现有工具集合；
- 任何加载的白名单包含未知工具都视为 Skill 配置错误；
- `load_skill` 和会话管理命令由运行时单独管理；
- 即使工具位于有效集合中，`ToolsManager.Execute` 仍必须调用 `PermissionManager.Authorize`。

因此，Skill 可用于降低误用工具的概率，但不能作为安全边界或权限授予机制。

## 9. 运行时结构

```go
type Registry interface {
    Reload(ctx context.Context) (Catalog, error)
    Catalog() Catalog
    Resolve(name string) (Definition, error)
}

type Definition struct {
    Name, Description, ArgumentHint string
    Mode                            Mode
    AllowedTools                    []string
    Body                            string
    Source                          SourceRef
}

type ActiveSkill struct {
    Definition Definition
    Arguments  string
    Rendered   string
    LoadedAt   time.Time
}
```

模块职责划分：

| 组件 | 职责 |
| --- | --- |
| `SkillRegistry` | 发现、解析、校验、去重、重载并提供 Catalog。 |
| `Renderer` | 进行受限的参数替换和目录内引用解析。 |
| `ActiveSkillStore` | 维护当前会话/任务的激活状态，并提供序列化数据。 |
| `LoadSkillTool` | 将 Registry 与 Agent 工具接口连接起来。 |
| `SkillRunner` | 为 Fork Skill 构造隔离 Agent、预算和结果摘要。 |
| `ContextManager` | 注入 Catalog 与 Inline 指令，并按有效工具集构建 `ContextView`。 |

## 10. 会话、审计与可观测性

`ActiveSkill` 不是隐式内存状态。每次加载、卸载、执行 Fork、重载失败均应追加到会话 transcript，并至少记录：名称、来源作用域、内容 SHA256、参数、模式、有效工具和结果状态。恢复会话时，若原 Skill 仍存在且内容 SHA256 一致，可恢复激活状态；否则标记为“已变更”，要求重新加载，避免用不同 SOP 静默续跑。

日志中不得记录 Skill 正文中可能包含的敏感内容。事件可包括：`skill.catalog_loaded`、`skill.loaded`、`skill.fork_started`、`skill.fork_finished`、`skill.rejected`。

## 11. 管理命令

REPL 提供以下命令：

```text
/skill list [--all]        列出有效 Skill；--all 同时显示被覆盖或无效项
/skill info <name>         显示来源、元数据、有效工具和校验状态
/skill reload              重新扫描目录并原子替换 Catalog
/skill active              显示当前任务已激活的 Inline Skill
/skill unload <name>       卸载当前任务中的 Inline Skill
```

`reload` 成功前使用旧 Catalog；扫描或解析失败不得清空已经可用的 Catalog。执行中的 Fork 不受后续重载影响，它使用启动时冻结的 `Definition`。

## 12. 错误处理与安全约束

- front matter 缺失、必填字段缺失、名称非法、模式非法和未知工具均为不可加载错误；
- 文件大小、正文大小、引用深度和引用总大小设置上限，防止异常上下文膨胀；
- 所有路径经 `filepath.EvalSymlinks` 与根目录边界校验；任何越界、符号链接或循环引用均拒绝；
- Skill 目录扫描、重载和加载必须并发安全；Catalog 采用不可变快照，避免模型请求中途看到半更新状态；
- 运行时故障向模型返回简短原因，详细诊断仅写日志/`/skill info`；
- Project Skill 属于工作区输入，必须按不可信文本处理，不能覆盖系统提示、权限策略或宿主配置。

## 13. 实施顺序与验收

建议按以下阶段实施：

1. ✅ 实现 `Definition`、front matter 解析、三层发现和覆盖规则；
2. ✅ 注册 `load_skill`，实现参数渲染、Inline 激活及 Agent 的 Catalog/工具过滤接入；
3. 将 ActiveSkill 的加载记录写入会话，补齐恢复与审计事件；
4. 增加 `skill list/info/reload` 管理命令；
5. 实现带预算、取消和结果摘要的 `SkillRunner`，启用 Fork 模式。

当前版本的默认发现目录为 `<workspace>/.agent/skills`、
`<user-config-dir>/ffcode/skills` 和 `<executable-dir>/skills`。无显式
`.agent/permission.yaml` 时，`load_skill` 默认处于允许列表；若项目提供了
自定义权限策略，则需显式允许 `load_skill`。

最小验收标准：

- 项目级同名 Skill 稳定覆盖用户级和内置级；
- 初始模型请求只包含 Catalog，不包含完整正文；
- `load_skill` 后的下一次模型请求能获得渲染后的 SOP；
- `allowedTools` 能缩小工具 Schema，但不能绕过权限拦截；
- 格式错误、路径越界、未知工具和重名能被明确拒绝；
- 重载不会破坏正在运行或已激活的 Skill；
- Fork 的取消、预算耗尽和工具拒绝能完整返回主 Agent。
