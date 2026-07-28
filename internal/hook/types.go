// Package hook provides the bounded lifecycle hook runtime used by the agent.
//
// The package deliberately has no dependency on agent, tool, or conversation
// packages.  Callers describe a lifecycle event with Input and the dispatcher
// takes care of ordering, limits, failure handling, and recursion protection.
package hook

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Event is a lifecycle point at which registered handlers may run.
type Event string

const (
	EventPreToolUse       Event = "pre_tool_use"
	EventPostToolUse      Event = "post_tool_use"
	EventSessionStart     Event = "session_start"
	EventUserPromptSubmit Event = "user_prompt_submit"
	EventStop             Event = "stop"
	EventPreCompact       Event = "pre_compact"
	EventPostCompact      Event = "post_compact"
	EventSubagentStart    Event = "subagent_start"
	EventSubagentStop     Event = "subagent_stop"

	// Short aliases keep the API pleasant for Go callers while the Event
	// values remain the stable snake_case wire names.
	PreToolUse       = EventPreToolUse
	PostToolUse      = EventPostToolUse
	SessionStart     = EventSessionStart
	UserPromptSubmit = EventUserPromptSubmit
	Stop             = EventStop
	PreCompact       = EventPreCompact
	PostCompact      = EventPostCompact
	SubagentStart    = EventSubagentStart
	SubagentStop     = EventSubagentStop
)

// AllEvents is the complete supported event set in lifecycle order.
var AllEvents = []Event{
	EventPreToolUse,
	EventPostToolUse,
	EventSessionStart,
	EventUserPromptSubmit,
	EventStop,
	EventPreCompact,
	EventPostCompact,
	EventSubagentStart,
	EventSubagentStop,
}

// ParseEvent validates a wire event name.
func ParseEvent(value string) (Event, error) {
	event := Event(strings.ToLower(strings.TrimSpace(value)))
	if !event.Valid() {
		return "", fmt.Errorf("unknown hook event %q", value)
	}
	return event, nil
}

// Valid reports whether e is a supported lifecycle event.
func (e Event) Valid() bool {
	switch e {
	case EventPreToolUse, EventPostToolUse, EventSessionStart,
		EventUserPromptSubmit, EventStop, EventPreCompact, EventPostCompact,
		EventSubagentStart, EventSubagentStop:
		return true
	default:
		return false
	}
}

func (e Event) String() string { return string(e) }

// Input is the JSON-compatible payload supplied to a hook handler.
// Fields are intentionally broad so the same shape can be used by command
// hooks and by in-process handlers at every lifecycle boundary.
type Input struct {
	Event      Event          `json:"event"`
	SessionID  string         `json:"session_id,omitempty"`
	Workspace  string         `json:"workspace,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ToolUseID  string         `json:"tool_use_id,omitempty"`
	Arguments  map[string]any `json:"arguments,omitempty"`
	ToolResult *ToolResult    `json:"tool_result,omitempty"`
	ToolOutput string         `json:"tool_output,omitempty"`
	Output     string         `json:"output,omitempty"`
	IsError    bool           `json:"is_error,omitempty"`
	UserPrompt string         `json:"user_prompt,omitempty"`
	Prompt     string         `json:"prompt,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Depth      int            `json:"depth,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}

// HookInput is kept as a named alias for callers that prefer the longer name.
type HookInput = Input

// ToolResult is the generic representation of a tool outcome used in hook
// payloads.  It is separate from tool.ToolResult to avoid an import cycle.
type ToolResult struct {
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

// Decision controls a hook's explicit lifecycle decision.
type Decision string

const (
	DecisionAllow    Decision = "allow"
	DecisionDeny     Decision = "deny"
	DecisionBlock    Decision = "block"
	DecisionContinue Decision = "continue"
	DecisionAsk      Decision = "ask"

	Allow    = DecisionAllow
	Deny     = DecisionDeny
	Block    = DecisionBlock
	Continue = DecisionContinue
)

// Output is returned by an in-process or command handler.
type Output struct {
	Decision          Decision       `json:"decision,omitempty"`
	Reason            string         `json:"reason,omitempty"`
	AdditionalContext string         `json:"additional_context,omitempty"`
	Context           string         `json:"context,omitempty"`
	UpdatedInput      map[string]any `json:"updated_input,omitempty"`
	Output            string         `json:"output,omitempty"`
	// Continue is accepted for compatibility with hook protocols that use a
	// boolean continuation field.  When false and Decision is empty, the
	// dispatcher treats the output as a denial for pre events.
	Continue  *bool `json:"continue,omitempty"`
	Truncated bool  `json:"truncated,omitempty"`
}

// HookOutput is a named alias for Output.
type HookOutput = Output

// Allows reports whether an output permits the operation to continue.
func (o Output) Allows(event Event) bool {
	decision := normalizeDecision(o.Decision)
	if isBeforeEvent(event) && (decision == DecisionDeny || decision == DecisionBlock || decision == DecisionAsk) {
		return false
	}
	if o.Continue != nil && !*o.Continue && isBeforeEvent(event) {
		return false
	}
	return true
}

func normalizeDecision(decision Decision) Decision {
	switch strings.ToLower(strings.TrimSpace(string(decision))) {
	case "allow", "approved", "approve", "continue", "ok":
		return DecisionAllow
	case "deny", "denied", "block", "blocked", "reject", "rejected", "ask":
		return DecisionDeny
	default:
		return ""
	}
}

func isBeforeEvent(event Event) bool {
	switch event {
	case EventPreToolUse, EventSessionStart, EventUserPromptSubmit, EventPreCompact, EventSubagentStart:
		return true
	default:
		return false
	}
}

// FailurePolicy controls what a handler failure does to the lifecycle action.
type FailurePolicy string

const (
	// FailureOpen records the failure and lets the action continue.
	FailureOpen FailurePolicy = "fail_open"
	// FailureClosed blocks before-events when a handler fails.  For after
	// events the action has already happened; callers receive the error but
	// cannot roll the side effect back.
	FailureClosed FailurePolicy = "fail_closed"
	// FailureAbort is an explicit alias for fail-closed semantics.
	FailureAbort FailurePolicy = "abort"

	FailOpen   = FailureOpen
	FailClosed = FailureClosed
	Abort      = FailureAbort
	// Friendly aliases used by configuration and older integrations.
	FailureIgnore FailurePolicy = FailureOpen
	FailureBlock  FailurePolicy = FailureClosed
)

// FailureStrategy is an alias retained for API compatibility.
type FailureStrategy = FailurePolicy

// CommandSpec describes an external command hook.
type CommandSpec struct {
	Command          string            `json:"command" yaml:"command"`
	Args             []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Dir              string            `json:"dir,omitempty" yaml:"dir,omitempty"`
	WorkingDirectory string            `json:"working_directory,omitempty" yaml:"working_directory,omitempty"`
	Env              map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	Shell            bool              `json:"shell,omitempty" yaml:"shell,omitempty"`
	Timeout          time.Duration     `json:"-" yaml:"-"`
	MaxOutputBytes   int               `json:"-" yaml:"-"`
}

// Handler processes one hook payload. Handlers are called in registration
// order and must honor ctx for cancellation where possible.
type Handler interface {
	Handle(context.Context, Input) (Output, error)
}

// HandlerFunc adapts a function to Handler.
type HandlerFunc func(context.Context, Input) (Output, error)

func (f HandlerFunc) Handle(ctx context.Context, input Input) (Output, error) {
	if f == nil {
		return Output{}, errors.New("nil hook handler")
	}
	return f(ctx, input)
}

// HookFunc is a compatibility alias for HandlerFunc.
type HookFunc = HandlerFunc

// Config controls dispatcher-wide safety limits.
type Config struct {
	Timeout        time.Duration           `json:"-" yaml:"-"`
	MaxOutputBytes int                     `json:"max_output_bytes,omitempty" yaml:"max_output_bytes,omitempty"`
	MaxDepth       int                     `json:"max_depth,omitempty" yaml:"max_depth,omitempty"`
	MaxInvocations int                     `json:"max_invocations,omitempty" yaml:"max_invocations,omitempty"`
	FailurePolicy  FailurePolicy           `json:"failure_policy,omitempty" yaml:"failure_policy,omitempty"`
	Policies       map[Event]FailurePolicy `json:"policies,omitempty" yaml:"policies,omitempty"`
	Hooks          map[Event][]CommandSpec `json:"hooks,omitempty" yaml:"hooks,omitempty"`
	Commands       map[Event][]CommandSpec `json:"commands,omitempty" yaml:"commands,omitempty"`
}

const (
	DefaultTimeout        = 5 * time.Second
	DefaultMaxOutputBytes = 64 * 1024
	DefaultMaxDepth       = 8
	DefaultMaxInvocations = 64
)

// DefaultConfig returns bounded defaults. Hook failures are fail-open except
// for pre_tool_use, which protects the permission boundary with fail-closed.
func DefaultConfig() Config {
	return Config{
		Timeout:        DefaultTimeout,
		MaxOutputBytes: DefaultMaxOutputBytes,
		MaxDepth:       DefaultMaxDepth,
		MaxInvocations: DefaultMaxInvocations,
		FailurePolicy:  FailureOpen,
		Policies: map[Event]FailurePolicy{
			EventPreToolUse: FailureClosed,
		},
	}
}

func (c Config) normalized() Config {
	d := DefaultConfig()
	policyExplicit := c.FailurePolicy != ""
	if c.Timeout <= 0 {
		c.Timeout = d.Timeout
	}
	if c.MaxOutputBytes <= 0 {
		c.MaxOutputBytes = d.MaxOutputBytes
	}
	if c.MaxDepth <= 0 {
		c.MaxDepth = d.MaxDepth
	}
	if c.MaxInvocations <= 0 {
		c.MaxInvocations = d.MaxInvocations
	}
	if c.FailurePolicy == "" {
		c.FailurePolicy = d.FailurePolicy
	}
	explicitPolicies := c.Policies
	c.Policies = make(map[Event]FailurePolicy, len(explicitPolicies)+len(d.Policies))
	if !policyExplicit {
		for event, policy := range d.Policies {
			c.Policies[event] = policy
		}
	}
	for event, policy := range explicitPolicies {
		c.Policies[event] = policy
	}
	if c.Hooks == nil {
		c.Hooks = make(map[Event][]CommandSpec)
	} else {
		c.Hooks = cloneCommandMap(c.Hooks)
	}
	if c.Commands == nil {
		c.Commands = make(map[Event][]CommandSpec)
	} else {
		c.Commands = cloneCommandMap(c.Commands)
	}
	return c
}

// Validate checks explicit safety values without applying defaults.
func (c Config) Validate() error {
	if c.Timeout < 0 {
		return errors.New("hook timeout cannot be negative")
	}
	if c.MaxOutputBytes < 0 {
		return errors.New("hook max output bytes cannot be negative")
	}
	if c.MaxDepth < 0 || c.MaxInvocations < 0 {
		return errors.New("hook recursion limits cannot be negative")
	}
	if c.FailurePolicy != "" && !validFailurePolicy(c.FailurePolicy) {
		return fmt.Errorf("unknown hook failure policy %q", c.FailurePolicy)
	}
	for event, policy := range c.Policies {
		if !event.Valid() {
			return fmt.Errorf("unknown hook policy event %q", event)
		}
		if !validFailurePolicy(policy) {
			return fmt.Errorf("unknown hook failure policy %q for %s", policy, event)
		}
	}
	for _, commands := range []map[Event][]CommandSpec{c.Hooks, c.Commands} {
		for event, specs := range commands {
			if !event.Valid() {
				return fmt.Errorf("unknown hook command event %q", event)
			}
			for index, spec := range specs {
				if strings.TrimSpace(spec.Command) == "" {
					return fmt.Errorf("hook %s command %d is empty", event, index)
				}
				if spec.Timeout < 0 || spec.MaxOutputBytes < 0 {
					return fmt.Errorf("hook %s command %d has negative limits", event, index)
				}
			}
		}
	}
	return nil
}

func validFailurePolicy(policy FailurePolicy) bool {
	switch policy {
	case FailureOpen, FailureClosed, FailureAbort:
		return true
	default:
		return false
	}
}

var (
	ErrRecursionLimit = errors.New("hook recursion limit exceeded")
	ErrOutputLimit    = errors.New("hook output limit exceeded")
	ErrHookTimeout    = errors.New("hook timed out")
	ErrHookDenied     = errors.New("hook denied operation")
	ErrInvalidEvent   = errors.New("invalid hook event")
	ErrInvalidHandler = errors.New("invalid hook handler")
	ErrInvalidOutput  = errors.New("invalid hook output")
)

// ExecutionError preserves the event and handler index while allowing callers
// to use errors.Is against the sentinel safety errors above.
type ExecutionError struct {
	Event   Event
	Index   int
	Handler string
	Cause   error
}

func (e *ExecutionError) Error() string {
	if e == nil {
		return "hook execution failed"
	}
	name := e.Handler
	if name == "" {
		name = fmt.Sprintf("handler-%d", e.Index)
	}
	return fmt.Sprintf("hook %s %s failed: %v", e.Event, name, e.Cause)
}

func (e *ExecutionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}
