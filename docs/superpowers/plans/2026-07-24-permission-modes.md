# 用户权限模式实现计划

> **面向 AI 代理的工作者：** 必需子技能：使用 superpowers:subagent-driven-development（推荐）或 superpowers:executing-plans 逐任务实现此计划。步骤使用复选框（`- [ ]`）语法来跟踪进度。

**目标：** 为 FFCode 增加“完全访问”“替我审批”“请求批准”三档权限模式，并支持用户配置、启动选择、运行时切换和 UI 状态展示。

**架构：** 保留现有静态 `Policy` 作为工具、能力和路径硬边界，在 `permission.Manager` 中增加并发安全的动态 `Mode`，只用它决定 Safe、Low、High 风险的自动允许、自动拒绝或人工批准行为。`internal/app` 负责配置转换和依赖装配，终端通过最小控制器接口选择、显示和切换模式，所有 Tool 仍从原有统一权限入口执行。

**技术栈：** Go 1.25、标准库 `context`/`flag`/`sync`/`testing`、YAML v3、Bubble Tea v2、现有 Permission 与 Terminal 模块

---

## 文件结构

- 创建 `internal/permission/mode.go`、`mode_test.go`：模式值、解析和授权矩阵。
- 修改 `internal/permission/manager.go`、`audit.go`：动态模式、缓存和审计。
- 修改 `internal/permission/confirm.go`，创建 `confirm_test.go`：串行化终端批准。
- 修改 `internal/config/config.go`，创建 `config_test.go`：用户默认模式。
- 修改 `internal/app/options.go`、`options_test.go`：启动参数。
- 创建 `internal/ui/terminal/permission_picker.go`、`permission_picker_test.go`：启动选择器。
- 修改 `internal/app/tools.go`、`bootstrap.go`、`app.go`：运行时装配。
- 创建 `internal/app/permission_mode_test.go`：配置模式、启动覆盖和非交互错误。
- 修改 `internal/ui/terminal/commands.go`，创建 `commands_test.go`：运行时查询与切换。
- 修改 `internal/ui/terminal/repl.go`、`repl_test.go`：欢迎区实时状态。
- 修改 `README.md`、`TODO.md`：用户文档和任务状态。

### 任务 1：定义权限模式并实现 Manager 决策矩阵

**文件：**
- 创建：`internal/permission/mode.go`
- 创建：`internal/permission/mode_test.go`
- 修改：`internal/permission/manager.go`

- [ ] **步骤 1：编写模式解析和风险矩阵的失败测试**

在 `mode_test.go` 添加：

```go
func TestParseMode(t *testing.T) {
	tests := []struct{ input string; want Mode; ok bool }{
		{"full_access", ModeFullAccess, true},
		{"full-access", ModeFullAccess, true},
		{"auto_approve", ModeAutoApprove, true},
		{"auto-approve", ModeAutoApprove, true},
		{"ask", ModeAsk, true},
		{"unsafe", "", false},
	}
	for _, test := range tests {
		got, ok := ParseMode(test.input)
		if got != test.want || ok != test.ok {
			t.Fatalf("ParseMode(%q) = (%q, %v), want (%q, %v)", test.input, got, ok, test.want, test.ok)
		}
	}
}

func TestManagerModeRiskMatrix(t *testing.T) {
	tests := []struct{ mode Mode; risk RiskLevel; want PermissionDecision }{
		{ModeFullAccess, Safe, Allow}, {ModeFullAccess, Low, Allow}, {ModeFullAccess, High, Allow},
		{ModeAutoApprove, Safe, Allow}, {ModeAutoApprove, Low, Allow}, {ModeAutoApprove, High, Deny},
		{ModeAsk, Safe, Allow}, {ModeAsk, Low, Allow}, {ModeAsk, High, Allow},
	}
	for _, test := range tests {
		workspace := t.TempDir()
		manager, err := NewManager(modeTestPolicy(workspace), WithMode(test.mode), WithConfirmer(fixedConfirmer(AllowOnce)))
		if err != nil { t.Fatal(err) }
		got, err := manager.Authorize(context.Background(), PermissionRequest{
			ToolName: "WriteFile", Action: "write", WorkingDirectory: workspace, RiskLevel: test.risk,
		})
		if err != nil { t.Fatal(err) }
		if got.Decision != test.want { t.Fatalf("mode %q risk %v = %q, want %q", test.mode, test.risk, got.Decision, test.want) }
	}
}
```

测试辅助对象使用显式允许写工具的 Policy，并让 `fixedConfirmer` 返回指定决定：

```go
func modeTestPolicy(workspace string) Policy {
	policy := DefaultPolicy(workspace)
	policy.Tools["writefile"] = ToolPolicy{Permission: Allow, ToolPermission: ToolPermission{CanWrite: true}}
	return policy
}

type fixedConfirmer ConfirmationDecision
func (f fixedConfirmer) Confirm(context.Context, PermissionRequest) (ConfirmationDecision, error) {
	return ConfirmationDecision(f), nil
}
```

- [ ] **步骤 2：运行测试并确认失败**

运行：`go test ./internal/permission -run 'Test(ParseMode|ManagerModeRiskMatrix)'`

预期：FAIL，提示 `Mode`、`ParseMode` 和 `WithMode` 未定义。

- [ ] **步骤 3：实现模式类型**

创建 `mode.go`：

```go
type Mode string
const (
	ModeFullAccess Mode = "full_access"
	ModeAutoApprove Mode = "auto_approve"
	ModeAsk Mode = "ask"
)
func ParseMode(value string) (Mode, bool) {
	mode := Mode(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_"))
	if !mode.Valid() { return "", false }
	return mode, true
}
func (m Mode) Valid() bool { return m == ModeFullAccess || m == ModeAutoApprove || m == ModeAsk }
func (m Mode) CLIName() string { return strings.ReplaceAll(string(m), "_", "-") }
```

- [ ] **步骤 4：给 Manager 增加并发安全的模式状态**

让已有 `mu` 同时保护 `mode` 与 `sessionAllowed`：

```go
func WithMode(mode Mode) Option { return func(m *Manager) { m.mode = mode } }

func (m *Manager) Mode() Mode {
	m.mu.RLock(); defer m.mu.RUnlock()
	return m.mode
}
func (m *Manager) SetMode(mode Mode) error {
	if !mode.Valid() { return fmt.Errorf("invalid permission mode %q", mode) }
	m.mu.Lock(); defer m.mu.Unlock()
	if m.mode != mode { m.mode = mode; clear(m.sessionAllowed) }
	return nil
}
```

`NewManager` 默认设置 `mode: ModeAsk`，应用 Options 后用 `Valid()` 拒绝非法模式。

- [ ] **步骤 5：用模式矩阵替换现有确认条件**

`Authorize` 开始时读取一次 `mode := m.Mode()`。Critical 拒绝之后按以下顺序决策：

```go
explicitConfirmation := toolDecision == Confirm || toolPolicy.RequireConfirm
if explicitConfirmation && mode != ModeAsk {
	return PermissionResult{Decision: Deny, Reason: "explicit user confirmation is unavailable in " + mode.CLIName() + " mode"}, nil
}
switch mode {
case ModeFullAccess:
	return PermissionResult{Decision: Allow, Reason: joinReasons("allowed by full-access mode", req.RiskReasons)}, nil
case ModeAutoApprove:
	if req.RiskLevel == High {
		return PermissionResult{Decision: Deny, Reason: joinReasons("high-risk operation denied by auto-approve mode", req.RiskReasons)}, nil
	}
	return PermissionResult{Decision: Allow, Reason: joinReasons("allowed by auto-approve mode", req.RiskReasons)}, nil
case ModeAsk:
	if req.RiskLevel == Safe && !explicitConfirmation {
		return PermissionResult{Decision: Allow, Reason: joinReasons("safe operation allowed by ask mode", req.RiskReasons)}, nil
	}
}
```

实际修改需保留当前命名返回值和审计 defer；上面的返回值应转换成给 `result` 赋值后 `return`。后续 Session 缓存与 Confirmer 分支只服务 `ask` 的 Low、High 和显式确认。

- [ ] **步骤 6：补齐硬规则和切换测试**

增加测试验证：所有 Mode 拒绝 Critical；静态 `Permission: Deny` 在 Full Access 下仍拒绝；`Permission: Confirm` 与 `RequireConfirm` 在自动模式下拒绝、Ask 下询问；`SetMode` 非法值不改变状态；任意模式切换都会清空 Session 批准缓存。

- [ ] **步骤 7：格式化、测试并提交**

```bash
gofmt -w internal/permission/mode.go internal/permission/mode_test.go internal/permission/manager.go
go test ./internal/permission
go test -race ./internal/permission
git add internal/permission/mode.go internal/permission/mode_test.go internal/permission/manager.go
git commit -m "feat: add runtime permission modes"
```

预期：测试全部 PASS，race detector 无报告。

### 任务 2：加载和校验用户默认权限模式

**文件：**
- 修改：`internal/config/config.go`
- 创建：`internal/config/config_test.go`

- [ ] **步骤 1：编写配置失败测试**

使用包含最小合法 `model` 段的临时 YAML，分别断言：未配置时为 `ask`；`full_access`、`auto_approve`、`ask` 均可加载；`unsafe` 返回包含 `permission.mode` 的错误。

```go
func TestLoadFileDefaultsPermissionModeToAsk(t *testing.T) {
	config, err := LoadFile(writeConfig(t, validModelYAML), &bytes.Buffer{})
	if err != nil { t.Fatal(err) }
	if config.Permission.Mode != "ask" { t.Fatalf("mode = %q", config.Permission.Mode) }
}

func TestLoadFileRejectsInvalidPermissionMode(t *testing.T) {
	_, err := LoadFile(writeConfig(t, validModelYAML+"permission:\n  mode: unsafe\n"), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "permission.mode") { t.Fatalf("error = %v", err) }
}

func TestPermissionModeHasNoEnvironmentOverride(t *testing.T) {
	t.Setenv("MYCODE_PERMISSION_MODE", "full_access")
	config, err := LoadFile(writeConfig(t, validModelYAML+"permission:\n  mode: ask\n"), &bytes.Buffer{})
	if err != nil { t.Fatal(err) }
	if config.Permission.Mode != "ask" { t.Fatalf("mode = %q, want ask", config.Permission.Mode) }
}
```

- [ ] **步骤 2：运行并确认 `Config.Permission` 未定义**

运行：`go test ./internal/config -run PermissionMode`

预期：FAIL，编译器提示 `Permission` 字段不存在。

- [ ] **步骤 3：实现配置结构、默认值和校验**

```go
const DefaultPermissionMode = "ask"
type PermissionConfig struct { Mode string `yaml:"mode"` }
```

给 `Config` 增加 `Permission PermissionConfig`；`applyDefaults` 在空值时设置 `ask`；`validate` 仅接受三个下划线形式的配置值：

```go
switch c.Permission.Mode {
case "full_access", "auto_approve", "ask":
default:
	return fmt.Errorf("permission.mode must be one of full_access, auto_approve, ask; got %q", c.Permission.Mode)
}
```

不要修改 `applyEnvironment`，权限模式没有环境变量入口。

- [ ] **步骤 4：格式化、测试并提交**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
go test ./internal/config
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat: configure default permission mode"
```

预期：PASS。

### 任务 3：让确认和审计适配动态模式

**文件：**
- 修改：`internal/permission/audit.go`
- 修改：`internal/permission/manager.go`
- 修改：`internal/permission/confirm.go`
- 创建：`internal/permission/confirm_test.go`

- [ ] **步骤 1：编写审计模式测试**

创建捕获 Logger，调用 Auto Approve 的 Low 风险请求，断言唯一 `AuditEntry.Mode == ModeAutoApprove`：

```go
type captureAudit struct{ entries []AuditEntry }
func (a *captureAudit) Log(entry AuditEntry) error { a.entries = append(a.entries, entry); return nil }

func TestAuthorizeAuditsPermissionMode(t *testing.T) {
	workspace := t.TempDir()
	logger := &captureAudit{}
	manager, err := NewManager(modeTestPolicy(workspace), WithMode(ModeAutoApprove), WithAuditLogger(logger))
	if err != nil { t.Fatal(err) }
	_, err = manager.Authorize(context.Background(), PermissionRequest{
		ToolName: "WriteFile", Action: "write", WorkingDirectory: workspace, RiskLevel: Low,
	})
	if err != nil { t.Fatal(err) }
	if len(logger.entries) != 1 || logger.entries[0].Mode != ModeAutoApprove { t.Fatalf("entries = %#v", logger.entries) }
}
```

- [ ] **步骤 2：编写并发确认测试**

创建一个首次读取阻塞的 Reader，同时从两个 goroutine 调用同一个 `TerminalConfirmer.Confirm`：

```go
type serializedReader struct {
	mu sync.Mutex
	reads int
	firstRelease chan struct{}
}
func (r *serializedReader) Read(p []byte) (int, error) {
	r.mu.Lock(); r.reads++; current := r.reads; r.mu.Unlock()
	if current == 1 { <-r.firstRelease }
	copy(p, "y\n")
	return 2, nil
}
func TestTerminalConfirmerSerializesPrompts(t *testing.T) {
	reader := &serializedReader{firstRelease: make(chan struct{})}
	confirmer := &TerminalConfirmer{In: reader, Out: &bytes.Buffer{}}
	done := make(chan struct{}, 2)
	for range 2 {
		go func() { _, _ = confirmer.Confirm(context.Background(), PermissionRequest{ToolName: "WriteFile"}); done <- struct{}{} }()
	}
	time.Sleep(20 * time.Millisecond)
	reader.mu.Lock(); reads := reader.reads; reader.mu.Unlock()
	if reads != 1 { t.Fatalf("reads before release = %d, want 1", reads) }
	close(reader.firstRelease)
	<-done; <-done
}
```

释放首次读取前只能发生一次 Reader 调用；未加锁的确认器会让测试失败。

- [ ] **步骤 3：实现模式审计和确认互斥**

给 `AuditEntry` 增加：

```go
Mode Mode `json:"mode"`
```

`Authorize` 的审计 defer 写入本次调用开头捕获的 Mode 快照。给 `TerminalConfirmer` 增加 `mu sync.Mutex`，在 nil 检查后、Context 与输入输出校验前加锁，使提示、读取、解析处在同一临界区。

- [ ] **步骤 4：格式化、竞态测试并提交**

```bash
gofmt -w internal/permission/audit.go internal/permission/manager.go internal/permission/confirm.go internal/permission/confirm_test.go
go test ./internal/permission
go test -race ./internal/permission
git add internal/permission/audit.go internal/permission/manager.go internal/permission/confirm.go internal/permission/confirm_test.go
git commit -m "feat: audit and serialize permission decisions"
```

预期：全部 PASS。

### 任务 4：解析启动参数并实现权限选择器

**文件：**
- 修改：`internal/app/options.go`
- 修改：`internal/app/options_test.go`
- 创建：`internal/ui/terminal/permission_picker.go`
- 创建：`internal/ui/terminal/permission_picker_test.go`

- [ ] **步骤 1：编写 CLI 参数失败测试**

```go
func TestParseOptionsCombinesWorkspaceAndPermissionPicker(t *testing.T) {
	got, err := parseOptions([]string{"--cwd", "cmd/FFCode", "--choose-permissions"})
	if err != nil { t.Fatal(err) }
	if got.Workspace != "cmd/FFCode" || !got.ChoosePermissions { t.Fatalf("options = %#v", got) }
}

func TestParseOptionsDefaultsPermissionPickerOff(t *testing.T) {
	got, err := parseOptions(nil)
	if err != nil { t.Fatal(err) }
	if got.ChoosePermissions { t.Fatal("permission picker unexpectedly enabled") }
}
```

- [ ] **步骤 2：实现参数解析和帮助文本**

在同一个 `flag.FlagSet` 中解析两个参数，删除只解析 Workspace 的重复路径：

```go
type Options struct {
	Workspace string
	ChoosePermissions bool
}

func parseOptions(arguments []string) (Options, error) {
	flags := flag.NewFlagSet("FFCode", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	workspace := flags.String("cwd", "", "workspace directory")
	choose := flags.Bool("choose-permissions", false, "choose permissions for this run")
	if err := flags.Parse(arguments); err != nil { return Options{}, err }
	if flags.NArg() != 0 { return Options{}, fmt.Errorf("unexpected argument %q", flags.Arg(0)) }
	return Options{Workspace: *workspace, ChoosePermissions: *choose}, nil
}
```

在 usage 中增加 `--choose-permissions  Choose the permission mode for this run`。

- [ ] **步骤 3：编写选择器 Model 失败测试**

直接驱动 Model，断言默认项、Down 后的下一项、Enter 完成和 Ctrl+C 取消：

```go
func TestPermissionPickerStartsAtConfiguredMode(t *testing.T) {
	model := newPermissionPickerModel(permission.ModeAutoApprove)
	if model.selectedMode() != permission.ModeAutoApprove { t.Fatalf("selected = %q", model.selectedMode()) }
}

func TestPermissionPickerSelectsNextMode(t *testing.T) {
	model := newPermissionPickerModel(permission.ModeFullAccess)
	next, _ := model.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if next.(permissionPickerModel).selectedMode() != permission.ModeAutoApprove { t.Fatal("next mode was not selected") }
}
```

- [ ] **步骤 4：实现 Bubble Tea 选择器**

定义固定顺序与说明：

```go
var permissionChoices = []struct { Mode permission.Mode; Name, Description string }{
	{permission.ModeFullAccess, "完全访问", "Safe、Low、High 自动允许；Critical 拒绝"},
	{permission.ModeAutoApprove, "替我审批", "Safe、Low 自动允许；High、Critical 拒绝"},
	{permission.ModeAsk, "请求批准", "Safe 自动允许；Low、High 请求批准；Critical 拒绝"},
}
```

Model 支持 `up`、`down`、`enter`、`ctrl+c`，非法初始值回退到 Ask。公开入口为：

```go
func ChoosePermissionMode(in io.Reader, out io.Writer, initial permission.Mode) (permission.Mode, error) {
	program := tea.NewProgram(newPermissionPickerModel(initial), tea.WithInput(in), tea.WithOutput(out))
	result, err := program.Run()
	if err != nil { return "", err }
	model, ok := result.(permissionPickerModel)
	if !ok { return "", errors.New("unexpected permission picker state") }
	if model.cancelled { return "", errPermissionPickerCancelled }
	return model.selectedMode(), nil
}
```

- [ ] **步骤 5：格式化、测试并提交**

```bash
gofmt -w internal/app/options.go internal/app/options_test.go internal/ui/terminal/permission_picker.go internal/ui/terminal/permission_picker_test.go
go test ./internal/app ./internal/ui/terminal
git add internal/app/options.go internal/app/options_test.go internal/ui/terminal/permission_picker.go internal/ui/terminal/permission_picker_test.go
git commit -m "feat: add permission mode startup picker"
```

预期：PASS。

### 任务 5：将选定模式接入应用运行时

**文件：**
- 修改：`internal/app/tools.go`
- 修改：`internal/app/bootstrap.go`
- 修改：`internal/app/app.go`
- 创建：`internal/app/permission_mode_test.go`

- [ ] **步骤 1：修改 Tool 创建接口**

```go
func createTools(ctx context.Context, workspace string, mode permission.Mode) (*tool.ToolsManager, *permission.Manager, func(), error)
```

创建权限 Manager 时传入 `permission.WithMode(mode)`。所有成功路径返回 Tool Manager、具体 Permission Manager、cleanup 和 nil；所有错误路径返回四个值。保持现有 Tool 注册、项目 Policy 加载和 MCP cleanup 行为不变。

- [ ] **步骤 2：让 bootstrap 保存控制器**

```go
type runtime struct {
	runner *agent.Agent
	permissions *permission.Manager
	contextManager *contextmanager.ContextManager
	sessions *session.Service
	cleanup func()
}

func bootstrap(ctx context.Context, config appconfig.Config, workspace, systemPrompt string, mode permission.Mode) (*runtime, error)
```

调用新 `createTools`，并在最终 Runtime 中保存返回的 Permission Manager。

- [ ] **步骤 3：编写启动模式解析失败测试**

把交互判断和选择函数作为参数注入，避免测试启动真实 TTY：

```go
func TestResolvePermissionModeRejectsNonInteractivePicker(t *testing.T) {
	_, err := resolvePermissionMode(Options{ChoosePermissions: true}, permission.ModeAsk, nil, &bytes.Buffer{}, false, nil)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") { t.Fatalf("error = %v", err) }
}

func TestResolvePermissionModeUsesPickerResult(t *testing.T) {
	chooser := func(io.Reader, io.Writer, permission.Mode) (permission.Mode, error) {
		return permission.ModeFullAccess, nil
	}
	got, err := resolvePermissionMode(Options{ChoosePermissions: true}, permission.ModeAsk, strings.NewReader(""), &bytes.Buffer{}, true, chooser)
	if err != nil { t.Fatal(err) }
	if got != permission.ModeFullAccess { t.Fatalf("mode = %q", got) }
}
```

- [ ] **步骤 4：实现配置转换、TTY 检查和本次覆盖**

在 `app.Run` 加载配置后执行：

```go
mode, ok := permission.ParseMode(config.Permission.Mode)
if !ok {
	fmt.Fprintf(stderr, "权限配置无效: %q\n", config.Permission.Mode)
	return 1
}
mode, err = resolvePermissionMode(
	options, mode, os.Stdin, stdout, stdinIsTerminal(os.Stdin), terminal.ChoosePermissionMode,
)
if err != nil { fmt.Fprintf(stderr, "权限选择失败: %v\n", err); return 1 }
```

辅助函数在未请求选择时直接返回 configured；请求选择但 `interactive == false` 时返回包含 `interactive terminal` 的错误；否则调用注入的 chooser。`stdinIsTerminal` 用 `os.File.Stat` 判断 `os.ModeCharDevice`。把最终 Mode 传给 bootstrap；不要修改 Config 或回写 YAML。

辅助函数签名固定为：

```go
type permissionChooser func(io.Reader, io.Writer, permission.Mode) (permission.Mode, error)

func resolvePermissionMode(options Options, configured permission.Mode, in io.Reader, out io.Writer, interactive bool, chooser permissionChooser) (permission.Mode, error) {
	if !options.ChoosePermissions { return configured, nil }
	if !interactive { return "", errors.New("--choose-permissions requires an interactive terminal") }
	if chooser == nil { return "", errors.New("permission chooser is unavailable") }
	return chooser(in, out, configured)
}
```

- [ ] **步骤 5：格式化、全仓库编译测试并提交**

```bash
gofmt -w internal/app/tools.go internal/app/bootstrap.go internal/app/app.go internal/app/permission_mode_test.go
go test ./internal/app
go test ./...
git add internal/app/tools.go internal/app/bootstrap.go internal/app/app.go internal/app/permission_mode_test.go
git commit -m "feat: wire permission mode into runtime"
```

预期：PASS。

### 任务 6：增加 `/permissions` 命令和实时 UI 状态

**文件：**
- 修改：`internal/ui/terminal/commands.go`
- 创建：`internal/ui/terminal/commands_test.go`
- 修改：`internal/ui/terminal/repl.go`
- 修改：`internal/ui/terminal/repl_test.go`
- 修改：`internal/app/app.go`

- [ ] **步骤 1：编写命令失败测试**

用内存控制器测试查看、合法切换和非法输入：

```go
type fakePermissionController struct{ mode permission.Mode }
func (c *fakePermissionController) Mode() permission.Mode { return c.mode }
func (c *fakePermissionController) SetMode(mode permission.Mode) error {
	if !mode.Valid() { return fmt.Errorf("invalid mode %q", mode) }
	c.mode = mode
	return nil
}

func TestPermissionsCommandChangesMode(t *testing.T) {
	controller := &fakePermissionController{mode: permission.ModeAsk}
	result := runPermissions(context.Background(), &CommandContext{Out: &bytes.Buffer{}, Permissions: controller}, "full-access")
	if result.Err != nil { t.Fatal(result.Err) }
	if controller.mode != permission.ModeFullAccess { t.Fatalf("mode = %q", controller.mode) }
}
```

另写两个测试：无参数输出包含 `auto-approve`；传 `unsafe` 返回错误且 Mode 仍为 Ask。

- [ ] **步骤 2：实现控制接口和命令**

```go
type PermissionController interface {
	Mode() permission.Mode
	SetMode(permission.Mode) error
}
```

给 `CommandContext` 增加 `Permissions`。注册命令：

```go
{Name: "permissions", Usage: "/permissions [full-access|auto-approve|ask]", Description: "查看或切换当前权限模式", Run: runPermissions}
```

`runPermissions` 无参数时输出 Mode 和风险说明；有参数时调用 `permission.ParseMode` 与 `SetMode`；非法参数返回 `usage: /permissions [full-access|auto-approve|ask]`，不得修改状态。

- [ ] **步骤 3：编写欢迎区实时状态测试**

```go
func TestWelcomeShowsCurrentPermissionMode(t *testing.T) {
	var output bytes.Buffer
	controller := &fakePermissionController{mode: permission.ModeAsk}
	printWelcomeTo(&output, "test-model", "/workspace", controller)
	plain := ansiSequence.ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "permission: ask") { t.Fatalf("welcome = %q", plain) }
	controller.mode = permission.ModeFullAccess
	output.Reset()
	printWelcomeTo(&output, "test-model", "/workspace", controller)
	plain = ansiSequence.ReplaceAllString(output.String(), "")
	if !strings.Contains(plain, "permission: full-access") { t.Fatalf("welcome = %q", plain) }
}
```

- [ ] **步骤 4：让 Runtime、欢迎区和 `/clear` 使用实时控制器**

给 `terminal.Runtime` 增加 `Permissions PermissionController` 并纳入 nil 校验。构建 `CommandContext` 时传入它。把欢迎函数改为：

```go
func printWelcomeTo(out io.Writer, modelName, workspace string, permissions PermissionController)
```

函数通过 `permissions.Mode().CLIName()` 输出 `permission:` 行。启动欢迎区和 `Clear` 回调每次都传入 Runtime 控制器。`app.Run` 创建 Terminal Runtime 时设置 `Permissions: runtime.permissions`。

- [ ] **步骤 5：格式化、测试并提交**

```bash
gofmt -w internal/ui/terminal/commands.go internal/ui/terminal/commands_test.go internal/ui/terminal/repl.go internal/ui/terminal/repl_test.go internal/app/app.go
go test ./internal/ui/terminal ./internal/app
go test ./...
git add internal/ui/terminal/commands.go internal/ui/terminal/commands_test.go internal/ui/terminal/repl.go internal/ui/terminal/repl_test.go internal/app/app.go
git commit -m "feat: show and switch permission modes"
```

预期：全部 PASS。

### 任务 7：文档、回归测试与验收

**文件：**
- 修改：`README.md`
- 修改：`TODO.md`

- [ ] **步骤 1：更新用户文档和 TODO**

在 README 记录以下配置和命令：

```yaml
permission:
  mode: ask
```

说明合法值为 `full_access`、`auto_approve`、`ask`；三档都保留 Workspace、受保护路径、项目 Policy 和 Critical 拦截。记录 `FFCode --choose-permissions`、`/permissions` 及三个切换参数。在 `TODO.md` 将第 3 项标记 `done`。

- [ ] **步骤 2：运行目标测试和竞态检测**

```bash
go test ./internal/config ./internal/permission ./internal/app ./internal/ui/terminal
go test -race ./internal/permission ./internal/tool
```

预期：全部 PASS，无 data race。

- [ ] **步骤 3：运行仓库级验证**

```bash
go test ./...
go vet ./...
git diff --check
```

预期：所有命令退出码为 0，无空白错误。

- [ ] **步骤 4：人工验收**

```bash
go run ./cmd/FFCode --help
go run ./cmd/FFCode --choose-permissions
go run ./cmd/FFCode --cwd . --choose-permissions
```

预期：帮助中出现新参数；选择器默认项来自配置；选择后欢迎区显示本次模式。进入应用后执行 `/permissions`、`/permissions ask`、`/clear`，状态立即更新且清屏后仍显示 Ask。

- [ ] **步骤 5：提交文档并确认工作区干净**

```bash
git add README.md TODO.md
git commit -m "docs: document permission modes"
git status --short
```

预期：提交成功，`git status --short` 无输出。
