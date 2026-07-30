# 用户权限模式设计

## 背景与目标

FFCode 已有统一的 Tool 权限入口、项目级工具策略、Workspace 路径边界、受保护路径、Shell 风险分析、用户确认和审计能力。当前应用没有面向用户的权限模式，默认策略也无法表达用户对交互频率的偏好。

本次改动增加三档运行时权限模式：`full_access`、`auto_approve` 和 `ask`。用户可以在用户级配置文件中设置默认模式，通过启动选择器覆盖本次运行的模式，并在终端 UI 中查看或切换当前模式。

权限模式只决定不同风险等级是否需要用户确认。它不能绕过项目策略、Workspace 边界、受保护路径、工具能力限制或 Critical 操作拦截。

## 非目标

- 不允许真正绕过权限网关。
- 不放开 Workspace 外部路径或受保护路径。
- 不允许任何模式执行 Critical 操作。
- 不通过环境变量配置权限模式。
- 不自动修改或回写用户配置文件。
- 不增加多 Profile 或项目级默认权限模式。

## 权限模式

在 `internal/permission` 中新增模式类型：

```go
type Mode string

const (
	ModeFullAccess  Mode = "full_access"
	ModeAutoApprove Mode = "auto_approve"
	ModeAsk         Mode = "ask"
)
```

三个模式对风险等级的处理如下：

| 模式 | Safe | Low | High | Critical |
| --- | --- | --- | --- | --- |
| `full_access` | 自动允许 | 自动允许 | 自动允许 | 拒绝 |
| `auto_approve` | 自动允许 | 自动允许 | 自动拒绝 | 拒绝 |
| `ask` | 自动允许 | 请求批准 | 请求批准 | 拒绝 |

模式名称的终端展示文案分别为“完全访问”“替我审批”和“请求批准”。配置文件和命令参数使用稳定的英文标识。

## 配置与优先级

用户级配置文件 `~/.ffcode/config.yaml` 增加：

```yaml
permission:
  mode: auto_approve
```

配置结构增加 `PermissionConfig`，并由 `Config` 持有：

```go
type PermissionConfig struct {
	Mode string `yaml:"mode"`
}
```

`internal/config` 保存并校验稳定的字符串配置值，保持配置层与权限运行时解耦。`internal/app` 在装配时将它转换为 `permission.Mode`。

未配置时默认使用 `ask`。未知值导致配置加载失败，错误信息包含字段名以及三个合法值。不增加权限模式对应的环境变量。

最终模式的优先级为：

```text
--choose-permissions 本次选择 > ~/.ffcode/config.yaml > ask
```

启动选择和运行时命令只修改当前进程，不回写配置文件。

## 授权架构

采用“静态 Policy + 动态 Mode”两层设计：

- `Policy` 继续描述工具是否可用、工具读写删除能力、允许与拒绝路径，以及显式确认要求。
- `Mode` 描述 Safe、Low、High 风险在当前运行中是自动允许、自动拒绝还是请求用户批准。
- `Manager` 是唯一决策入口，持有当前模式并提供并发安全的读取与切换方法。

`Manager` 增加：

```go
func (m *Manager) Mode() Mode
func (m *Manager) SetMode(mode Mode) error
```

模式读写需要并发安全，因为同一轮 Agent 可能并行执行多个工具。设置非法模式必须返回错误并保留原模式。

每次 `Authorize` 开始时读取一次模式快照，后续决策与审计均使用同一个值，避免运行时切换造成单次授权记录前后不一致。

## 授权顺序

每次 Tool 调用继续统一经过 `PermissionManager.Authorize`：

1. 校验请求、工具名称和基础风险等级。
2. 应用 `.agent/permission.yaml` 的工具 allowlist 和显式 deny。
3. 校验工具读写删除能力。
4. 分析 Shell 命令并合并最终风险等级与原因。
5. 解析并校验工作目录、目标路径、Workspace 边界、受保护路径和工具路径规则。
6. Critical 操作直接拒绝。
7. 处理工具策略中的 `require_confirm`。
8. 根据当前 Mode 和最终风险等级允许、拒绝或请求批准。

静态 deny、能力限制、路径限制和 Critical 拒绝始终优先，任何模式均不能覆盖。

当工具规则设置 `permission: confirm` 或 `require_confirm: true` 时：

- `ask` 模式请求用户批准。
- `full_access` 和 `auto_approve` 模式自动拒绝。

这样显式要求人工确认的项目策略不会被自动模式静默放行。

## 用户批准与会话缓存

`ask` 模式沿用现有批准选择：允许一次、当前 Session 允许和拒绝。Safe 操作不弹出批准提示。

从任意其他模式切换到 `ask` 时，清空 Manager 内的 Session 级批准缓存，保证进入请求批准模式后，旧状态不会跳过人工确认。从 `ask` 切换到其他模式时也不保留可在之后恢复的隐藏批准状态。

Agent 会并行执行同一批 Tool 调用。`TerminalConfirmer` 必须串行化确认提示和输入读取，避免多个批准请求同时写入终端或竞争标准输入。等待确认时仍应检查 Context；输入失败或 Context 取消时拒绝当前操作。

## 启动选择器

新增启动参数：

```bash
FFCode --choose-permissions
```

没有该参数时直接使用用户配置的默认模式，不显示选择器。存在该参数时，在创建 Agent 和 Tool Manager 前使用现有 Bubble Tea 技术栈显示三项选择器，默认选中配置文件中的模式：

- 完全访问
- 替我审批
- 请求批准

选择结果仅覆盖当前进程。`Ctrl+C` 取消选择并终止启动。在标准输入不是交互终端时使用该参数应返回明确错误，不能静默使用默认模式。

`--choose-permissions` 可以与 `--cwd` 同时使用。参数解析继续拒绝未知参数和多余位置参数。

## 运行时命令

默认命令注册表增加：

```text
/permissions
/permissions full-access
/permissions auto-approve
/permissions ask
```

为保持 CLI 书写习惯，命令接受连字符形式；内部统一映射到配置值 `full_access`、`auto_approve`、`ask`。

- 无参数时显示当前模式及 Safe、Low、High 的处理方式。
- 带合法参数时切换当前进程的模式并显示结果。
- 非法参数返回完整用法，模式保持不变。
- 命令仅在两轮 Agent 对话之间执行，因此不会修改已经开始的 Tool 授权决策。

终端层定义最小的 `PermissionController` 接口，不直接依赖 Manager 的具体实现。`createTools` 在返回 Tool Manager 的同时返回它创建的 `permission.Manager`；应用装配层把该 Manager 作为控制器传给终端运行时和命令上下文。Tool 的执行入口仍只持有 `PermissionManager` 授权接口。

## UI 展示

欢迎区增加当前权限模式：

```text
model: <model>
directory: <workspace>
permission: auto-approve
```

`/clear` 重新绘制欢迎区时必须通过控制器读取实时模式，不能使用启动时保存的旧字符串。执行 `/permissions` 切换后，新的模式立即成为后续 Tool 调用的授权依据。

选择器和帮助信息展示中文名称及简短的行为说明；欢迎区使用简洁、稳定的英文标识，方便用户核对配置值。

## 审计

审计记录增加 `mode` 字段，保存做出决策时读取到的模式。现有的 Tool、参数、命令、最终 decision、risk、reasons、user 和 duration 字段保持不变。

自动拒绝的 Reason 需要区分以下来源：

- 项目策略或工具能力拒绝。
- Workspace、受保护路径或 Critical 硬规则拒绝。
- `auto_approve` 对 High 风险的自动拒绝。
- 自动模式遇到 `require_confirm` 后的拒绝。
- 用户在 `ask` 模式中的拒绝。

## 错误处理

- 配置中的未知模式导致启动失败，不回退到其他模式。
- 启动选择器无法使用交互终端时返回错误。
- `SetMode` 收到非法值时返回错误且不改变当前模式。
- 审批器缺失、输入失败或 Context 取消时，待确认操作拒绝执行。
- 授权错误继续作为普通 Tool 错误结果返回给模型，不能终止整个 Agent 循环。

## 测试策略

### 配置测试

- 未配置时得到 `ask`。
- 三个合法配置值正确加载。
- 非法配置值包含清晰的字段错误。
- 验证权限模式不会被任何环境变量覆盖。

### 权限包测试

- 覆盖三档模式的 Safe、Low、High 决策矩阵。
- 所有模式均拒绝 Critical。
- 静态 deny、能力限制和路径限制始终优先。
- 三档模式正确处理 `require_confirm`。
- 合法模式可切换，非法模式不改变状态。
- 切换模式会清空 Session 级批准缓存。
- 并发读取和切换模式不存在数据竞争。
- 终端确认请求被串行处理。
- 审计记录包含决策时的模式。

### 终端测试

- `/permissions` 显示当前模式。
- 三个合法参数可切换模式。
- 非法参数保持原模式。
- 欢迎区展示当前模式。
- `/clear` 展示切换后的模式。
- 启动选择器默认选中配置模式，并可选择三档模式。
- 非交互终端拒绝 `--choose-permissions`。

### 应用测试

- `--choose-permissions` 与 `--cwd` 可组合解析。
- 配置模式正确传递给权限 Manager。
- 选择器结果覆盖配置且不写回文件。

完成后运行：

```bash
go test ./...
go vet ./...
```

涉及并发状态与确认串行化的权限包测试应额外运行：

```bash
go test -race ./internal/permission ./internal/tool
```

## 实施边界

本功能预计涉及：

- `internal/config`：配置结构、默认值和校验。
- `internal/permission`：Mode、决策矩阵、动态切换、确认串行化和审计字段。
- `internal/app`：CLI 参数、启动选择器和运行时装配。
- `internal/ui/terminal`：欢迎区、`/permissions` 命令和选择器 UI。

实现不应重写现有 Policy 文件格式，也不应让 UI 直接承担授权决策。
