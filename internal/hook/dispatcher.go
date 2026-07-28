package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

type contextKey uint8

const (
	stateKey contextKey = iota
	inputKey
	skipKey
)

type dispatchState struct {
	depth       int
	invocations *atomic.Int64
	active      map[Event]bool
}

func (s *dispatchState) child(event Event) *dispatchState {
	child := &dispatchState{depth: s.depth + 1, invocations: s.invocations, active: make(map[Event]bool, len(s.active)+1)}
	for active, value := range s.active {
		child.active[active] = value
	}
	child.active[event] = true
	return child
}

func (s *dispatchState) invocationCount() int64 {
	if s == nil || s.invocations == nil {
		return 0
	}
	return s.invocations.Load()
}

func (s *dispatchState) reserveInvocation(limit int) bool {
	if s.invocations == nil {
		s.invocations = &atomic.Int64{}
	}
	return s.invocations.Add(1) <= int64(limit)
}

func stateFromContext(ctx context.Context) *dispatchState {
	if ctx == nil {
		ctx = context.Background()
	}
	if state, ok := ctx.Value(stateKey).(*dispatchState); ok && state != nil {
		return state
	}
	depth := 0
	if raw := strings.TrimSpace(os.Getenv("MYCODE_HOOK_DEPTH")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			depth = parsed
		}
	}
	invocations := &atomic.Int64{}
	invocations.Store(int64(depth))
	return &dispatchState{depth: depth, invocations: invocations, active: make(map[Event]bool)}
}

// WithInput attaches lifecycle information to a context so lower-level
// adapters (notably ToolsManager) can enrich their hook payloads without
// importing one another.
func WithInput(ctx context.Context, input Input) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	input = cloneInput(input)
	return context.WithValue(ctx, inputKey, input)
}

// InputFromContext returns the nearest lifecycle input attached to ctx.
func InputFromContext(ctx context.Context) (Input, bool) {
	if ctx == nil {
		return Input{}, false
	}
	input, ok := ctx.Value(inputKey).(Input)
	if !ok {
		return Input{}, false
	}
	return cloneInput(input), true
}

// WithDepth is useful for adapters that start a hook chain outside a
// Dispatcher. Nested dispatches still share the invocation budget.
func WithDepth(ctx context.Context, depth int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if depth < 0 {
		depth = 0
	}
	state := stateFromContext(ctx)
	copyState := &dispatchState{depth: depth, invocations: state.invocations, active: make(map[Event]bool, len(state.active))}
	for event, active := range state.active {
		copyState.active[event] = active
	}
	return context.WithValue(ctx, stateKey, copyState)
}

// Depth reports the current hook nesting depth.
func Depth(ctx context.Context) int { return stateFromContext(ctx).depth }

// WithoutHooks marks a context so adapters can intentionally suppress hooks
// for internal housekeeping. This is opt-in; recursion protection remains the
// default and cannot be bypassed accidentally by a normal context.
func WithoutHooks(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, skipKey, true)
}

func hooksSkipped(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	value, _ := ctx.Value(skipKey).(bool)
	return value
}

type registeredHandler struct {
	handler Handler
	name    string
}

// Result is the aggregate result of a dispatch.
type Result struct {
	Event        Event
	Outputs      []Output
	Output       string
	Decision     Decision
	Reason       string
	UpdatedInput map[string]any
	Failures     []error
	Blocked      bool
	Skipped      bool
	Truncated    bool
}

// HookResult is a compatibility alias for Result.
type HookResult = Result

// Allowed reports whether no handler explicitly blocked the operation.
func (r Result) Allowed() bool { return !r.Blocked }

// Failed reports whether one or more handlers failed under a fail-open policy.
func (r Result) Failed() bool { return len(r.Failures) > 0 }

// Dispatcher stores handlers and executes them in deterministic registration
// order. Registration is safe while other goroutines dispatch; each dispatch
// uses an immutable snapshot and never holds the registry lock during user
// code.
type Dispatcher struct {
	mu        sync.RWMutex
	config    Config
	handlers  map[Event][]registeredHandler
	onceMu    sync.Mutex
	once      map[string]*onceDispatch
	onceOrder []string
}

const maxOnceCacheEntries = 1024

type onceDispatch struct {
	done   chan struct{}
	result Result
	err    error
}

// Manager is an alias for callers that use manager terminology.
type Manager = Dispatcher

// New creates a bounded dispatcher. Invalid zero values are replaced with
// defaults; use Config.Validate when configuration errors must be surfaced.
func New(config Config) *Dispatcher {
	config = config.normalized()
	d := &Dispatcher{config: config, handlers: make(map[Event][]registeredHandler), once: make(map[string]*onceDispatch)}
	for event, specs := range config.Hooks {
		for _, spec := range specs {
			_ = d.RegisterCommand(event, spec)
		}
	}
	for event, specs := range config.Commands {
		for _, spec := range specs {
			_ = d.RegisterCommand(event, spec)
		}
	}
	return d
}

// NewDispatcher is the explicit constructor spelling.
func NewDispatcher(config Config) *Dispatcher { return New(config) }

// NewManager is a constructor alias.
func NewManager(config Config) *Dispatcher { return New(config) }

// NewValidated constructs a dispatcher while surfacing invalid configuration.
func NewValidated(config Config) (*Dispatcher, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return New(config), nil
}

// Config returns a copy of the active dispatcher configuration.
func (d *Dispatcher) Config() Config {
	if d == nil {
		return DefaultConfig()
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	config := d.config
	config.Policies = clonePolicies(config.Policies)
	config.Hooks = cloneCommandMap(config.Hooks)
	config.Commands = cloneCommandMap(config.Commands)
	return config
}

// SetConfig updates limits for subsequent dispatches. Existing handlers are
// retained. Values are normalized so a live dispatcher always remains bounded.
func (d *Dispatcher) SetConfig(config Config) {
	if d == nil {
		return
	}
	config = config.normalized()
	d.mu.Lock()
	d.config = config
	d.mu.Unlock()
}

// Register adds an in-process handler. The any parameter intentionally accepts
// common function forms in addition to Handler, making integrations concise:
// HandlerFunc, func(context.Context, Input) (Output, error), func(Input), and
// their pointer-input variants are supported.
func (d *Dispatcher) Register(event Event, candidate any) error {
	if d == nil {
		return errors.New("hook dispatcher is nil")
	}
	if !event.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidEvent, event)
	}
	handler, name, err := adaptHandler(candidate)
	if err != nil {
		return err
	}
	d.mu.Lock()
	d.handlers[event] = append(d.handlers[event], registeredHandler{handler: handler, name: name})
	d.mu.Unlock()
	return nil
}

// Add is a shorthand for Register.
func (d *Dispatcher) Add(event Event, candidate any) error { return d.Register(event, candidate) }

// RegisterHandler is the typed registration spelling.
func (d *Dispatcher) RegisterHandler(event Event, handler Handler) error {
	return d.Register(event, handler)
}

// RegisterCommand registers an external command for an event.
func (d *Dispatcher) RegisterCommand(event Event, spec CommandSpec) error {
	if d == nil {
		return errors.New("hook dispatcher is nil")
	}
	if !event.Valid() {
		return fmt.Errorf("%w: %q", ErrInvalidEvent, event)
	}
	if strings.TrimSpace(spec.Command) == "" {
		return errors.New("hook command is required")
	}
	d.mu.RLock()
	config := d.config
	d.mu.RUnlock()
	if spec.MaxOutputBytes <= 0 {
		spec.MaxOutputBytes = config.MaxOutputBytes
	}
	handler := &commandHandler{spec: cloneCommandSpec(spec)}
	d.mu.Lock()
	d.handlers[event] = append(d.handlers[event], registeredHandler{handler: handler, name: spec.Command})
	d.mu.Unlock()
	return nil
}

// Clear removes all handlers for event. It is primarily useful for tests and
// dynamic configuration reloads.
func (d *Dispatcher) Clear(event Event) {
	if d == nil {
		return
	}
	d.mu.Lock()
	delete(d.handlers, event)
	d.mu.Unlock()
}

// Dispatch executes all handlers registered for event.
func (d *Dispatcher) Dispatch(ctx context.Context, event Event, input Input) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{Event: event}, err
	}
	if d == nil {
		return Result{Event: event}, nil
	}
	if !event.Valid() {
		return Result{Event: event}, fmt.Errorf("%w: %q", ErrInvalidEvent, event)
	}
	if hooksSkipped(ctx) {
		return Result{Event: event, Skipped: true}, nil
	}
	input = cloneInput(input)
	input.Event = event
	state := stateFromContext(ctx)
	d.mu.RLock()
	config := d.config
	handlers := append([]registeredHandler(nil), d.handlers[event]...)
	d.mu.RUnlock()
	policy := policyFromConfig(config, event)
	result := Result{Event: event}
	remainingOutputBytes := config.MaxOutputBytes
	if len(handlers) == 0 {
		return result, nil
	}
	if state.depth >= config.MaxDepth || state.active[event] || !state.reserveInvocation(config.MaxInvocations) {
		result.Skipped = true
		failure, _ := boundErrorDiagnostic(ErrRecursionLimit, remainingOutputBytes)
		result.Failures = []error{failure}
		if policyBlocks(policy) {
			result.Blocked = true
			executionErr, _ := boundErrorDiagnostic(&ExecutionError{Event: event, Index: -1, Cause: failure}, config.MaxOutputBytes)
			return result, executionErr
		}
		return result, nil
	}
	childState := state.child(event)
	ctx = context.WithValue(ctx, stateKey, childState)
	input.Depth = childState.depth
	ctx = WithInput(ctx, input)

	for index, registered := range handlers {
		if err := ctx.Err(); err != nil {
			failure, used := boundErrorDiagnostic(err, remainingOutputBytes)
			remainingOutputBytes -= used
			result.Failures = append(result.Failures, failure)
			return result, err
		}
		output, err := d.invoke(ctx, config, registered, input)
		if parentErr := ctx.Err(); parentErr != nil {
			failure, used := boundErrorDiagnostic(parentErr, remainingOutputBytes)
			remainingOutputBytes -= used
			result.Failures = append(result.Failures, failure)
			return result, parentErr
		}
		var budgetErr error
		output, budgetErr = boundOutput(output, remainingOutputBytes)
		remainingOutputBytes -= outputBudgetBytes(output)
		if budgetErr != nil && !errors.Is(err, budgetErr) {
			err = errors.Join(err, budgetErr)
		}
		if updatedErr := validateUpdatedInput(event, output.UpdatedInput); updatedErr != nil && !errors.Is(err, updatedErr) {
			err = errors.Join(err, updatedErr)
		}
		if err != nil {
			var used int
			err, used = boundErrorDiagnostic(err, remainingOutputBytes)
			remainingOutputBytes -= used
			if output.Truncated || outputText(output) != "" || output.Reason != "" {
				result.Outputs = append(result.Outputs, output)
				result.Truncated = result.Truncated || output.Truncated
				if text := outputText(output); text != "" {
					if result.Output != "" {
						result.Output += "\n"
					}
					var aggregateTruncated bool
					result.Output, aggregateTruncated = boundedString(result.Output+text, config.MaxOutputBytes)
					result.Truncated = result.Truncated || aggregateTruncated
				}
				if output.Reason != "" {
					result.Reason = output.Reason
				}
			}
			var handledErr error
			result, handledErr = d.handleFailure(event, index, registered.name, result, err, policy, config.MaxOutputBytes)
			if handledErr != nil {
				return result, handledErr
			}
			continue
		}
		if output.Truncated {
			result.Truncated = true
		}
		if output.UpdatedInput != nil {
			result.UpdatedInput = mergeMap(result.UpdatedInput, output.UpdatedInput)
			input = applyUpdatedInput(input, output.UpdatedInput)
			ctx = WithInput(ctx, input)
		}
		result.Outputs = append(result.Outputs, output)
		if text := outputText(output); text != "" {
			if result.Output != "" {
				result.Output += "\n"
			}
			var aggregateTruncated bool
			result.Output, aggregateTruncated = boundedString(result.Output+text, config.MaxOutputBytes)
			result.Truncated = result.Truncated || aggregateTruncated
		}
		if reason := strings.TrimSpace(output.Reason); reason != "" {
			result.Reason = reason
		}
		decision := normalizeDecision(output.Decision)
		denied := decision == DecisionDeny || decision == DecisionBlock || decision == DecisionAsk
		if isBeforeEvent(event) && (denied || (output.Continue != nil && !*output.Continue)) {
			result.Blocked = true
			result.Decision = DecisionDeny
			if result.Reason == "" {
				result.Reason = "hook denied operation"
			}
			return result, nil
		}
		if decision != "" {
			result.Decision = decision
		}
	}
	if result.UpdatedInput != nil {
		result.UpdatedInput = canonicalUpdatedInput(event, input, result.UpdatedInput)
		bounded, err := boundOutput(Output{UpdatedInput: result.UpdatedInput}, config.MaxOutputBytes)
		result.UpdatedInput = bounded.UpdatedInput
		if err != nil {
			result.Truncated = true
			failure, used := boundErrorDiagnostic(err, remainingOutputBytes)
			remainingOutputBytes -= used
			last := len(handlers) - 1
			return d.handleFailure(event, last, handlers[last].name, result, failure, policy, config.MaxOutputBytes)
		}
	}
	if err := ctx.Err(); err != nil {
		failure, used := boundErrorDiagnostic(err, remainingOutputBytes)
		remainingOutputBytes -= used
		result.Failures = append(result.Failures, failure)
		return result, err
	}
	return result, nil
}

// Run and Trigger are compatibility spellings for Dispatch.
func (d *Dispatcher) Run(ctx context.Context, event Event, input Input) (Result, error) {
	return d.Dispatch(ctx, event, input)
}

func (d *Dispatcher) Trigger(ctx context.Context, event Event, input Input) (Result, error) {
	return d.Dispatch(ctx, event, input)
}

// DispatchOnce runs an event once for key and returns the cached result for
// subsequent calls. It is used for session_start, where multiple UI layers may
// observe the same session transition.
func (d *Dispatcher) DispatchOnce(ctx context.Context, event Event, key string, input Input) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{Event: event}, err
	}
	if d == nil {
		return Result{Event: event}, nil
	}
	state := stateFromContext(ctx)
	config := d.Config()
	if state.active[event] || state.depth >= config.MaxDepth || state.invocationCount() >= int64(config.MaxInvocations) {
		return d.Dispatch(ctx, event, input)
	}
	cacheKey := string(event) + "\x00" + key
	d.onceMu.Lock()
	if pending, ok := d.once[cacheKey]; ok {
		d.onceMu.Unlock()
		select {
		case <-pending.done:
			if err := ctx.Err(); err != nil {
				return Result{Event: event, Skipped: true}, err
			}
			cached := cloneResult(pending.result)
			cached.Skipped = true
			return cached, pending.err
		case <-ctx.Done():
			return Result{Event: event, Skipped: true}, ctx.Err()
		}
	}
	d.evictCompletedOnceLocked()
	pending := &onceDispatch{done: make(chan struct{})}
	d.once[cacheKey] = pending
	d.onceOrder = append(d.onceOrder, cacheKey)
	d.onceMu.Unlock()
	result, err := d.Dispatch(ctx, event, input)
	if err == nil {
		err = ctx.Err()
	}
	d.onceMu.Lock()
	pending.result = cloneResult(result)
	pending.err = err
	close(pending.done)
	if err != nil || result.Failed() {
		// Failed starts may be retried by a future explicit session transition.
		delete(d.once, cacheKey)
	}
	d.onceMu.Unlock()
	return result, err
}

// ResetOnce forgets a once key, allowing a session lifecycle to be replayed.
func (d *Dispatcher) ResetOnce(event Event, key string) {
	if d == nil {
		return
	}
	d.onceMu.Lock()
	delete(d.once, string(event)+"\x00"+key)
	d.onceMu.Unlock()
}

func (d *Dispatcher) evictCompletedOnceLocked() {
	if len(d.once) < maxOnceCacheEntries && len(d.onceOrder) < 2*maxOnceCacheEntries {
		return
	}
	kept := d.onceOrder[:0]
	for _, key := range d.onceOrder {
		pending, ok := d.once[key]
		if !ok {
			continue
		}
		if len(d.once) >= maxOnceCacheEntries {
			select {
			case <-pending.done:
				delete(d.once, key)
				continue
			default:
			}
		}
		kept = append(kept, key)
	}
	d.onceOrder = kept
}

func (d *Dispatcher) invoke(parent context.Context, config Config, registered registeredHandler, input Input) (Output, error) {
	handler := registered.handler
	timeout := config.Timeout
	if command, ok := handler.(*commandHandler); ok {
		spec := cloneCommandSpec(command.spec)
		if spec.Timeout > 0 && (timeout <= 0 || spec.Timeout < timeout) {
			timeout = spec.Timeout
		}
		if spec.MaxOutputBytes <= 0 || spec.MaxOutputBytes > config.MaxOutputBytes {
			spec.MaxOutputBytes = config.MaxOutputBytes
		}
		handler = &commandHandler{spec: spec}
	}
	ctx := parent
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(parent, timeout)
		defer cancel()
	}
	type response struct {
		output Output
		err    error
	}
	responses := make(chan response, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				if cause, ok := recovered.(error); ok {
					responses <- response{err: fmt.Errorf("hook handler panic: %w", cause)}
					return
				}
				responses <- response{err: fmt.Errorf("hook handler panic: %v", recovered)}
			}
		}()
		output, err := handler.Handle(ctx, input)
		responses <- response{output: output, err: err}
	}()
	select {
	case response := <-responses:
		if err := parent.Err(); err != nil {
			return Output{}, err
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			output, outputErr := normalizeOutput(response.output, config.MaxOutputBytes)
			return output, errors.Join(ErrHookTimeout, outputErr)
		}
		output, outputErr := normalizeOutput(response.output, config.MaxOutputBytes)
		if response.err != nil && outputErr != nil {
			if errors.Is(response.err, outputErr) {
				return output, response.err
			}
			return output, errors.Join(response.err, outputErr)
		}
		if response.err != nil {
			return output, response.err
		}
		return output, outputErr
	case <-ctx.Done():
		if err := parent.Err(); err != nil {
			return Output{}, err
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Output{}, ErrHookTimeout
		}
		return Output{}, ctx.Err()
	}
}

func (d *Dispatcher) handleFailure(event Event, index int, name string, result Result, err error, policy FailurePolicy, limit int) (Result, error) {
	if err == nil {
		return result, nil
	}
	result.Failures = append(result.Failures, err)
	if policy == FailureOpen {
		return result, nil
	}
	result.Blocked = true
	executionErr, _ := boundErrorDiagnostic(&ExecutionError{Event: event, Index: index, Handler: name, Cause: err}, limit)
	return result, executionErr
}

func policyFromConfig(config Config, event Event) FailurePolicy {
	if policy, ok := config.Policies[event]; ok && policy != "" {
		return policy
	}
	return config.FailurePolicy
}

func policyBlocks(policy FailurePolicy) bool {
	return policy == FailureClosed || policy == FailureAbort
}

func adaptHandler(candidate any) (Handler, string, error) {
	if candidate == nil {
		return nil, "", fmt.Errorf("%w: nil", ErrInvalidHandler)
	}
	if handler, ok := candidate.(Handler); ok {
		return handler, handlerName(handler), nil
	}
	switch fn := candidate.(type) {
	case func(context.Context, Input) (Output, error):
		return HandlerFunc(fn), functionName(fn), nil
	case func(Input) (Output, error):
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return fn(input) }), functionName(fn), nil
	case func(context.Context, Input) error:
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { return Output{}, fn(ctx, input) }), functionName(fn), nil
	case func(Input) error:
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return Output{}, fn(input) }), functionName(fn), nil
	case func(context.Context, Input) Output:
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { return fn(ctx, input), nil }), functionName(fn), nil
	case func(Input) Output:
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return fn(input), nil }), functionName(fn), nil
	case func(context.Context, Input):
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { fn(ctx, input); return Output{}, nil }), functionName(fn), nil
	case func(Input):
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { fn(input); return Output{}, nil }), functionName(fn), nil
	case func(context.Context, *Input) (Output, error):
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { return fn(ctx, &input) }), functionName(fn), nil
	case func(*Input) (Output, error):
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return fn(&input) }), functionName(fn), nil
	case func(context.Context, *Input) error:
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { return Output{}, fn(ctx, &input) }), functionName(fn), nil
	case func(*Input) error:
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return Output{}, fn(&input) }), functionName(fn), nil
	case func(context.Context, *Input) Output:
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { return fn(ctx, &input), nil }), functionName(fn), nil
	case func(*Input) Output:
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { return fn(&input), nil }), functionName(fn), nil
	case func(context.Context, *Input):
		return HandlerFunc(func(ctx context.Context, input Input) (Output, error) { fn(ctx, &input); return Output{}, nil }), functionName(fn), nil
	case func(*Input):
		return HandlerFunc(func(_ context.Context, input Input) (Output, error) { fn(&input); return Output{}, nil }), functionName(fn), nil
	default:
		return nil, "", fmt.Errorf("%w: %T", ErrInvalidHandler, candidate)
	}
}

func handlerName(handler Handler) string {
	if named, ok := handler.(interface{ Name() string }); ok {
		return named.Name()
	}
	return reflect.TypeOf(handler).String()
}

func functionName(fn any) string {
	if fn == nil {
		return ""
	}
	value := reflect.ValueOf(fn)
	if value.Kind() == reflect.Func {
		if name := reflect.ValueOf(fn).Pointer(); name != 0 {
			if function := runtime.FuncForPC(name); function != nil {
				return function.Name()
			}
		}
	}
	return reflect.TypeOf(fn).String()
}

func cloneInput(input Input) Input {
	input.Arguments = cloneMap(input.Arguments)
	input.Metadata = cloneMap(input.Metadata)
	if input.ToolResult != nil {
		copyResult := *input.ToolResult
		input.ToolResult = &copyResult
	}
	return input
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneValue(value)
	}
	return output
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case map[string]string:
		result := make(map[string]string, len(typed))
		for key, item := range typed {
			result[key] = item
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func mergeMap(left, right map[string]any) map[string]any {
	result := cloneMap(left)
	if result == nil {
		result = make(map[string]any, len(right))
	}
	for key, value := range right {
		result[key] = cloneValue(value)
	}
	return result
}

func applyUpdatedInput(input Input, updated map[string]any) Input {
	if input.Event == EventPreToolUse {
		if value, ok := updatedValue(updated, "arguments", "args", "tool_input"); ok {
			if arguments, ok := value.(map[string]any); ok {
				input.Arguments = cloneMap(arguments)
			}
		}
		if input.Arguments == nil {
			input.Arguments = make(map[string]any)
		}
		for key, value := range updated {
			switch strings.ToLower(key) {
			case "arguments", "args", "tool_input":
				continue
			default:
				input.Arguments[key] = cloneValue(value)
			}
		}
		return input
	}
	if value, ok := updatedValue(updated, "arguments", "args", "tool_input"); ok {
		if arguments, ok := value.(map[string]any); ok {
			input.Arguments = cloneMap(arguments)
		}
	}
	if value, ok := updatedValue(updated, "user_prompt", "prompt"); ok {
		if prompt, ok := value.(string); ok {
			input.UserPrompt, input.Prompt = prompt, prompt
		}
	}
	if value, ok := updatedValue(updated, "tool_name"); ok {
		if name, ok := value.(string); ok {
			input.ToolName = name
		}
	}
	return input
}

func canonicalUpdatedInput(event Event, input Input, updated map[string]any) map[string]any {
	result := cloneMap(updated)
	switch event {
	case EventPreToolUse:
		if _, wrapped := updatedValue(updated, "arguments", "args", "tool_input"); wrapped {
			return map[string]any{"arguments": cloneMap(input.Arguments)}
		}
	case EventUserPromptSubmit:
		if _, changed := updatedValue(updated, "user_prompt", "prompt"); changed {
			for key := range result {
				if strings.EqualFold(key, "user_prompt") || strings.EqualFold(key, "prompt") {
					delete(result, key)
				}
			}
			result["prompt"] = input.Prompt
		}
	}
	return result
}

func updatedValue(updated map[string]any, keys ...string) (any, bool) {
	for _, expected := range keys {
		for key, value := range updated {
			if strings.EqualFold(key, expected) {
				return value, true
			}
		}
	}
	return nil, false
}

func validateUpdatedInput(event Event, updated map[string]any) error {
	for key, value := range updated {
		switch event {
		case EventPreToolUse:
			switch strings.ToLower(key) {
			case "arguments", "args", "tool_input":
				if _, ok := value.(map[string]any); !ok {
					return fmt.Errorf("%w: %s must be an object", ErrInvalidOutput, key)
				}
			}
		case EventUserPromptSubmit:
			switch strings.ToLower(key) {
			case "user_prompt", "prompt":
				if _, ok := value.(string); !ok {
					return fmt.Errorf("%w: %s must be a string", ErrInvalidOutput, key)
				}
			}
		}
	}
	return nil
}

func outputText(output Output) string {
	if strings.TrimSpace(output.Output) != "" {
		return output.Output
	}
	if strings.TrimSpace(output.AdditionalContext) != "" {
		return output.AdditionalContext
	}
	return output.Context
}

func normalizeOutput(output Output, limit int) (Output, error) {
	output.UpdatedInput = cloneMap(output.UpdatedInput)
	var outputErr error
	if output.Continue != nil {
		continued := *output.Continue
		output.Continue = &continued
	}
	if rawDecision := strings.TrimSpace(string(output.Decision)); rawDecision != "" {
		normalized := normalizeDecision(output.Decision)
		if normalized == "" {
			outputErr = fmt.Errorf("%w: unknown decision %q", ErrInvalidOutput, rawDecision)
		} else {
			output.Decision = normalized
		}
	}
	output, boundErr := boundOutput(output, limit)
	return output, errors.Join(outputErr, boundErr)
}

func boundOutput(output Output, limit int) (Output, error) {
	if limit < 0 {
		return output, nil
	}
	remaining := limit
	truncated := output.Truncated
	fields := []*string{&output.Output, &output.AdditionalContext, &output.Context, &output.Reason}
	for _, field := range fields {
		if *field == "" {
			continue
		}
		if remaining <= 0 {
			*field = ""
			truncated = true
			continue
		}
		bounded, wasTruncated := boundedString(*field, remaining)
		*field = bounded
		remaining -= len(bounded)
		truncated = truncated || wasTruncated
	}
	if output.UpdatedInput != nil {
		encoded, err := json.Marshal(output.UpdatedInput)
		if err != nil || len(encoded) > remaining {
			output.UpdatedInput = nil
			truncated = true
		} else {
			remaining -= len(encoded)
		}
	}
	output.Truncated = truncated
	if truncated {
		return output, ErrOutputLimit
	}
	return output, nil
}

func outputBudgetBytes(output Output) int {
	total := len(output.Output) + len(output.AdditionalContext) + len(output.Context) + len(output.Reason)
	if output.UpdatedInput != nil {
		if encoded, err := json.Marshal(output.UpdatedInput); err == nil {
			total += len(encoded)
		}
	}
	return total
}

func boundedString(value string, limit int) (string, bool) {
	if limit <= 0 || (len(value) <= limit && utf8.ValidString(value)) {
		return value, false
	}
	const marker = "\n[hook output truncated]"
	contentLimit := min(len(value), limit)
	if limit > len(marker) {
		contentLimit = min(contentLimit, limit-len(marker))
	} else {
		contentLimit = 0
	}
	cut := value[:contentLimit]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	if limit <= len(marker) {
		return marker[:limit], true
	}
	return cut + marker, true
}

func cloneCommandSpec(spec CommandSpec) CommandSpec {
	spec.Args = append([]string(nil), spec.Args...)
	if spec.Env != nil {
		environment := spec.Env
		spec.Env = make(map[string]string, len(spec.Env))
		for key, value := range environment {
			spec.Env[key] = value
		}
	}
	return spec
}

func clonePolicies(policies map[Event]FailurePolicy) map[Event]FailurePolicy {
	if policies == nil {
		return nil
	}
	copyPolicies := make(map[Event]FailurePolicy, len(policies))
	for event, policy := range policies {
		copyPolicies[event] = policy
	}
	return copyPolicies
}

func cloneCommandMap(commands map[Event][]CommandSpec) map[Event][]CommandSpec {
	if commands == nil {
		return nil
	}
	result := make(map[Event][]CommandSpec, len(commands))
	for event, specs := range commands {
		result[event] = make([]CommandSpec, len(specs))
		for index, spec := range specs {
			result[event][index] = cloneCommandSpec(spec)
		}
	}
	return result
}

func cloneResult(result Result) Result {
	outputs := make([]Output, len(result.Outputs))
	for index, output := range result.Outputs {
		outputs[index] = output
		outputs[index].UpdatedInput = cloneMap(output.UpdatedInput)
		if output.Continue != nil {
			continued := *output.Continue
			outputs[index].Continue = &continued
		}
	}
	result.Outputs = outputs
	result.Failures = append([]error(nil), result.Failures...)
	result.UpdatedInput = cloneMap(result.UpdatedInput)
	return result
}
