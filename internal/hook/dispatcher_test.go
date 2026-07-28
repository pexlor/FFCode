package hook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestDispatcherRunsHandlersInOrderAndCarriesUpdatedInput(t *testing.T) {
	dispatcher := New(DefaultConfig())
	var calls []string
	if err := dispatcher.Register(EventPreToolUse, func(_ context.Context, input Input) (Output, error) {
		calls = append(calls, "first:"+input.ToolName)
		return Output{UpdatedInput: map[string]any{"arguments": map[string]any{"path": "updated"}}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(EventPreToolUse, func(_ context.Context, input Input) (Output, error) {
		calls = append(calls, "second:"+input.Arguments["path"].(string))
		return Output{Decision: DecisionAllow, AdditionalContext: "checked"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{
		ToolName: "ReadFile", Arguments: map[string]any{"path": "original"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Blocked || result.Output != "checked" {
		t.Fatalf("result = %+v", result)
	}
	if got := strings.Join(calls, ","); got != "first:ReadFile,second:updated" {
		t.Fatalf("call order = %q", got)
	}
	arguments, ok := result.UpdatedInput["arguments"].(map[string]any)
	if !ok || arguments["path"] != "updated" {
		t.Fatalf("updated input = %#v", result.UpdatedInput)
	}
}

func TestDispatcherCarriesUpdatedInputThroughHandlerContext(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: map[string]any{"arguments": map[string]any{"path": "updated"}}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(EventPreToolUse, func(ctx context.Context, input Input) (Output, error) {
		fromContext, ok := InputFromContext(ctx)
		if !ok || fromContext.Arguments["path"] != input.Arguments["path"] {
			return Output{}, fmt.Errorf("context input=%+v handler input=%+v", fromContext, input)
		}
		return Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{
		Arguments: map[string]any{"path": "original"},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherCombinesWrappedAndDirectToolArgumentUpdates(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: map[string]any{
			"arguments": map[string]any{"first": true, "value": "wrapped"},
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(EventPreToolUse, func(_ context.Context, input Input) (Output, error) {
		if input.Arguments["value"] != "wrapped" {
			return Output{}, fmt.Errorf("second handler arguments = %+v", input.Arguments)
		}
		return Output{UpdatedInput: map[string]any{"value": "direct", "prompt": "tool argument"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(EventPreToolUse, func(_ context.Context, input Input) (Output, error) {
		if input.Arguments["value"] != "direct" || input.Arguments["prompt"] != "tool argument" {
			return Output{}, fmt.Errorf("third handler arguments = %+v", input.Arguments)
		}
		return Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{
		Arguments: map[string]any{"value": "original"},
	})
	if err != nil {
		t.Fatal(err)
	}
	arguments, ok := result.UpdatedInput["arguments"].(map[string]any)
	if !ok || arguments["first"] != true || arguments["value"] != "direct" || arguments["prompt"] != "tool argument" {
		t.Fatalf("updated input = %+v", result.UpdatedInput)
	}
}

func TestDispatcherDirectArgumentPatchDoesNotEchoUnchangedInput(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 32})
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: map[string]any{"value": "updated"}}
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{
		Arguments: map[string]any{"large": strings.Repeat("x", 256), "value": "original"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, wrapped := result.UpdatedInput["arguments"]; wrapped || result.UpdatedInput["value"] != "updated" {
		t.Fatalf("updated input echoed unchanged arguments: %+v", result.UpdatedInput)
	}
}

func TestDispatcherCanonicalUpdatedInputStaysWithinOutputBudget(t *testing.T) {
	tests := []struct {
		name      string
		event     Event
		input     Input
		updated   map[string]any
		canonical string
		alias     string
	}{
		{
			name: "tool arguments", event: EventPreToolUse, input: Input{Arguments: map[string]any{}},
			updated: map[string]any{"args": map[string]any{"x": "123456"}}, canonical: "arguments", alias: "args",
		},
		{
			name: "user prompt", event: EventUserPromptSubmit, input: Input{Prompt: "original", UserPrompt: "original"},
			updated: map[string]any{"user_prompt": "1234567890"}, canonical: "prompt", alias: "user_prompt",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := New(Config{MaxOutputBytes: 32})
			if err := dispatcher.Register(test.event, func(Input) Output {
				return Output{UpdatedInput: test.updated}
			}); err != nil {
				t.Fatal(err)
			}

			result, err := dispatcher.Dispatch(context.Background(), test.event, test.input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result.UpdatedInput)
			if err != nil {
				t.Fatal(err)
			}
			if len(encoded) > 32 {
				t.Fatalf("canonical updated input uses %d bytes: %s", len(encoded), encoded)
			}
			if _, ok := result.UpdatedInput[test.canonical]; !ok {
				t.Fatalf("canonical key %q is missing: %+v", test.canonical, result.UpdatedInput)
			}
			if _, ok := result.UpdatedInput[test.alias]; ok {
				t.Fatalf("alias key %q was retained: %+v", test.alias, result.UpdatedInput)
			}
		})
	}
}

func TestDispatcherDirectPatchSupportsToolArgumentsNamedPrompt(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: map[string]any{"prompt": "tool prompt", "tool_name": "argument value"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := dispatcher.Register(EventPreToolUse, func(_ context.Context, input Input) (Output, error) {
		if input.Arguments["prompt"] != "tool prompt" || input.Arguments["tool_name"] != "argument value" {
			return Output{}, fmt.Errorf("arguments = %+v", input.Arguments)
		}
		if input.Prompt != "lifecycle prompt" || input.ToolName != "actual-tool" {
			return Output{}, fmt.Errorf("lifecycle input changed: %+v", input)
		}
		return Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{
		ToolName: "actual-tool", Prompt: "lifecycle prompt", Arguments: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestDispatcherAdaptsDocumentedHandlerFunctions(t *testing.T) {
	var calls int
	candidates := []any{
		func(context.Context, Input) { calls++ },
		func(Input) { calls++ },
		func(context.Context, *Input) error { calls++; return nil },
		func(*Input) error { calls++; return nil },
		func(context.Context, *Input) Output { calls++; return Output{} },
		func(*Input) Output { calls++; return Output{} },
		func(context.Context, *Input) { calls++ },
		func(*Input) { calls++ },
	}
	dispatcher := New(DefaultConfig())
	for _, candidate := range candidates {
		if err := dispatcher.Register(EventPostToolUse, candidate); err != nil {
			t.Fatalf("register %T: %v", candidate, err)
		}
	}
	if _, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{}); err != nil {
		t.Fatal(err)
	}
	if calls != len(candidates) {
		t.Fatalf("handler calls = %d, want %d", calls, len(candidates))
	}
}

func TestDispatcherExplicitDenyIsNotAHandlerFailure(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, HandlerFunc(func(context.Context, Input) (Output, error) {
		return Output{Decision: DecisionDeny, Reason: "policy"}, nil
	})); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if err != nil {
		t.Fatalf("explicit denial returned an execution error: %v", err)
	}
	if !result.Blocked || result.Reason != "policy" || len(result.Failures) != 0 {
		t.Fatalf("result = %+v", result)
	}
}

func TestDispatcherRejectsUnknownDecisionUsingFailurePolicy(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 32})
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{Decision: Decision("dney"), Output: strings.Repeat("x", 128)}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if !errors.Is(err, ErrInvalidOutput) || !errors.Is(err, ErrOutputLimit) || !result.Blocked ||
		len(result.Outputs) != 1 || len(result.Outputs[0].Output) > 32 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestDispatcherRejectsInvalidUpdatedInputShape(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: map[string]any{"arguments": "not an object"}}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if !errors.Is(err, ErrInvalidOutput) || !result.Blocked {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPostEventDenialCannotUndoCompletedOperation(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPostToolUse, func(Input) Output {
		return Output{Decision: DecisionDeny, Reason: "too late"}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil || result.Blocked || !result.Allowed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Decision != DecisionDeny || result.Reason != "too late" {
		t.Fatalf("post decision was not recorded: %+v", result)
	}
}

func TestDispatcherTimeoutHonorsFailurePolicies(t *testing.T) {
	blocking := HandlerFunc(func(ctx context.Context, _ Input) (Output, error) {
		<-ctx.Done()
		return Output{}, ctx.Err()
	})

	closed := New(Config{Timeout: 15 * time.Millisecond, FailurePolicy: FailureClosed})
	if err := closed.Register(EventPreToolUse, blocking); err != nil {
		t.Fatal(err)
	}
	result, err := closed.Dispatch(context.Background(), EventPreToolUse, Input{})
	if !errors.Is(err, ErrHookTimeout) || !result.Blocked {
		t.Fatalf("closed timeout result=%+v err=%v", result, err)
	}

	open := New(Config{Timeout: 15 * time.Millisecond, FailurePolicy: FailureOpen, Policies: map[Event]FailurePolicy{EventPreToolUse: FailureOpen}})
	if err := open.Register(EventPreToolUse, blocking); err != nil {
		t.Fatal(err)
	}
	result, err = open.Dispatch(context.Background(), EventPreToolUse, Input{})
	if err != nil || result.Blocked || !result.Failed() || !errors.Is(result.Failures[0], ErrHookTimeout) {
		t.Fatalf("open timeout result=%+v err=%v", result, err)
	}
}

func TestSetConfigOnlyAffectsSubsequentDispatches(t *testing.T) {
	dispatcher := New(DefaultConfig())
	started := make(chan struct{})
	release := make(chan struct{})
	wantErr := errors.New("handler failed")
	if err := dispatcher.Register(EventPreToolUse, func(Input) error {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
		return wantErr
	}); err != nil {
		t.Fatal(err)
	}
	type dispatchOutcome struct {
		result Result
		err    error
	}
	done := make(chan dispatchOutcome, 1)
	go func() {
		result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
		done <- dispatchOutcome{result: result, err: err}
	}()
	<-started
	dispatcher.SetConfig(Config{
		FailurePolicy: FailureOpen,
		Policies:      map[Event]FailurePolicy{EventPreToolUse: FailureOpen},
	})
	close(release)
	current := <-done
	if !current.result.Blocked || !errors.Is(current.err, wantErr) {
		t.Fatalf("in-flight dispatch used new policy: result=%+v err=%v", current.result, current.err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if err != nil || result.Blocked || !result.Failed() {
		t.Fatalf("subsequent dispatch did not use new policy: result=%+v err=%v", result, err)
	}
}

func TestDispatcherBoundsOutputByBytesAndKeepsUTF8Valid(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 32, FailurePolicy: FailureOpen})
	if err := dispatcher.Register(EventPostToolUse, func(Input) Output {
		return Output{Output: strings.Repeat("界", 30)}
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.Failed() {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Output) > 32 || !utf8.ValidString(result.Output) {
		t.Fatalf("bounded output len=%d valid=%v output=%q", len(result.Output), utf8.ValidString(result.Output), result.Output)
	}
}

func TestNormalizeOutputRepairsUTF8SplitAtCommandLimit(t *testing.T) {
	raw := string([]byte{0xe7, 0x95, 0x8c, 0xe7})
	output, err := normalizeOutput(Output{Output: raw, Truncated: true}, len(raw))
	if !errors.Is(err, ErrOutputLimit) || !output.Truncated {
		t.Fatalf("output=%+v err=%v", output, err)
	}
	if len(output.Output) > len(raw) || !utf8.ValidString(output.Output) {
		t.Fatalf("invalid bounded output %q", output.Output)
	}
}

func TestDispatcherBoundsOutputReturnedWithHandlerError(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 32, FailurePolicy: FailureOpen})
	wantErr := errors.New("handler failed")
	if err := dispatcher.Register(EventPostToolUse, func(Input) (Output, error) {
		return Output{Output: strings.Repeat("x", 128)}, wantErr
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || len(result.Outputs[0].Output) > 32 || !result.Truncated {
		t.Fatalf("unbounded failed output: %+v", result)
	}
	if len(result.Failures) != 1 || !errors.Is(result.Failures[0], wantErr) || !errors.Is(result.Failures[0], ErrOutputLimit) {
		t.Fatalf("failures = %v", result.Failures)
	}
}

func TestDispatcherUsesOneOutputBudgetAcrossHandlers(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 32, FailurePolicy: FailureOpen})
	for range 3 {
		if err := dispatcher.Register(EventPostToolUse, func(Input) Output {
			return Output{Output: strings.Repeat("x", 24)}
		}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, output := range result.Outputs {
		total += outputBudgetBytes(output)
	}
	if total > 32 || !result.Truncated || !result.Failed() {
		t.Fatalf("total=%d result=%+v", total, result)
	}
}

func TestDispatcherClonesHandlerUpdatedInput(t *testing.T) {
	updated := map[string]any{"arguments": map[string]any{"path": "original"}}
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventPreToolUse, func(Input) Output {
		return Output{UpdatedInput: updated}
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	updated["arguments"].(map[string]any)["path"] = "mutated"
	resultArguments := result.UpdatedInput["arguments"].(map[string]any)
	outputArguments := result.Outputs[0].UpdatedInput["arguments"].(map[string]any)
	if resultArguments["path"] != "original" || outputArguments["path"] != "original" {
		t.Fatalf("handler mutation escaped into result: %+v", result)
	}
}

func TestDispatcherPreventsSameEventRecursion(t *testing.T) {
	dispatcher := New(Config{MaxDepth: 4, MaxInvocations: 8, FailurePolicy: FailureOpen})
	var nested Result
	if err := dispatcher.Register(EventPostToolUse, func(ctx context.Context, input Input) (Output, error) {
		var err error
		nested, err = dispatcher.Dispatch(ctx, EventPostToolUse, input)
		return Output{}, err
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil || result.Blocked {
		t.Fatalf("outer result=%+v err=%v", result, err)
	}
	if !nested.Skipped || !nested.Failed() || !errors.Is(nested.Failures[0], ErrRecursionLimit) {
		t.Fatalf("nested result = %+v", nested)
	}
}

func TestDispatcherMaxInvocationsAccumulatesAcrossSequentialNestedDispatches(t *testing.T) {
	dispatcher := New(Config{MaxDepth: 8, MaxInvocations: 2, FailurePolicy: FailureOpen})
	var stopCalls int
	if err := dispatcher.Register(EventStop, func(Input) Output {
		stopCalls++
		return Output{}
	}); err != nil {
		t.Fatal(err)
	}
	var nested []Result
	if err := dispatcher.Register(EventPostToolUse, func(ctx context.Context, input Input) (Output, error) {
		for range 3 {
			result, err := dispatcher.Dispatch(ctx, EventStop, input)
			if err != nil {
				return Output{}, err
			}
			nested = append(nested, result)
		}
		return Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{}); err != nil {
		t.Fatal(err)
	}
	if stopCalls != 1 {
		t.Fatalf("stop handler calls = %d, want 1", stopCalls)
	}
	if len(nested) != 3 || nested[0].Skipped || !nested[1].Skipped || !nested[2].Skipped {
		t.Fatalf("nested results = %+v", nested)
	}
	for _, result := range nested[1:] {
		if !result.Failed() || !errors.Is(result.Failures[0], ErrRecursionLimit) {
			t.Fatalf("limit result = %+v", result)
		}
	}
}

func TestDispatchOnceIsAtomicAcrossConcurrentCallers(t *testing.T) {
	dispatcher := New(DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(EventSessionStart, func(context.Context, Input) (Output, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return Output{Output: "started"}, nil
	}); err != nil {
		t.Fatal(err)
	}

	const workers = 12
	start := make(chan struct{})
	var wg sync.WaitGroup
	errorsCh := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			result, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, "session-1", Input{SessionID: "session-1"})
			if err != nil || result.Output != "started" {
				errorsCh <- fmt.Errorf("result=%+v err=%v", result, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("handler calls = %d, want 1", got)
	}
}

func TestParseConfigAcceptsScalarAndStructuredCommands(t *testing.T) {
	config, err := ParseConfig([]byte(`
timeout: 250ms
max_output_bytes: 128
policies:
  pre_tool_use: fail_closed
hooks:
  session_start: echo started
  post_tool_use:
    - command: printf
      args: [done]
      timeout: 100ms
`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Timeout != 250*time.Millisecond || config.MaxOutputBytes != 128 {
		t.Fatalf("config = %+v", config)
	}
	if len(config.Hooks[EventSessionStart]) != 1 || !config.Hooks[EventSessionStart][0].Shell {
		t.Fatalf("session hooks = %+v", config.Hooks[EventSessionStart])
	}
	post := config.Hooks[EventPostToolUse]
	if len(post) != 1 || post[0].Timeout != 100*time.Millisecond {
		t.Fatalf("post hooks = %+v", post)
	}
}

func TestParseConfigRejectsUnknownCommandFields(t *testing.T) {
	_, err := ParseConfig([]byte(`
hooks:
  post_tool_use:
    command: echo
    max_output_byte: 16
`))
	if err == nil || !strings.Contains(err.Error(), "max_output_byte") {
		t.Fatalf("error = %v", err)
	}
}

func TestParseConfigGlobalPolicyOverridesDefaultPreToolPolicy(t *testing.T) {
	config, err := ParseConfig([]byte("failure_policy: fail_open\n"))
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := New(config)
	if err := dispatcher.Register(EventPreToolUse, func(Input) error { return errors.New("ignored") }); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if err != nil || result.Blocked || !result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestPerEventPolicyPreservesDefaultPreToolPolicy(t *testing.T) {
	configs := map[string]Config{
		"go config": {
			Policies: map[Event]FailurePolicy{EventStop: FailureClosed},
		},
	}
	yamlConfig, err := ParseConfig([]byte("policies:\n  stop: fail_closed\n"))
	if err != nil {
		t.Fatal(err)
	}
	configs["yaml config"] = yamlConfig

	for name, config := range configs {
		t.Run(name, func(t *testing.T) {
			dispatcher := New(config)
			wantErr := errors.New("pre hook failed")
			if err := dispatcher.Register(EventPreToolUse, func(Input) error {
				return wantErr
			}); err != nil {
				t.Fatal(err)
			}
			result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
			if !errors.Is(err, wantErr) || !result.Blocked {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestDispatcherConfigReturnsDeepCopy(t *testing.T) {
	dispatcher := New(Config{Hooks: map[Event][]CommandSpec{
		EventStop: {{Command: "notify", Args: []string{"original"}, Env: map[string]string{"MODE": "original"}}},
	}})

	config := dispatcher.Config()
	config.Hooks[EventStop][0].Args[0] = "changed"
	config.Hooks[EventStop][0].Env["MODE"] = "changed"
	delete(config.Hooks, EventStop)

	current := dispatcher.Config()
	specs := current.Hooks[EventStop]
	if len(specs) != 1 || specs[0].Args[0] != "original" || specs[0].Env["MODE"] != "original" {
		t.Fatalf("dispatcher config was mutated through returned copy: %+v", current)
	}
}

func TestDispatchOnceRecursionReturnsWithoutDeadlock(t *testing.T) {
	dispatcher := New(Config{Timeout: 100 * time.Millisecond, FailurePolicy: FailureOpen})
	var nested Result
	if err := dispatcher.Register(EventSessionStart, func(ctx context.Context, input Input) (Output, error) {
		var err error
		nested, err = dispatcher.DispatchOnce(ctx, EventSessionStart, "same", input)
		return Output{}, err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, "same", Input{}); err != nil {
		t.Fatal(err)
	}
	if !nested.Skipped || !nested.Failed() || !errors.Is(nested.Failures[0], ErrRecursionLimit) {
		t.Fatalf("nested = %+v", nested)
	}
}

func TestDispatchOnceEvictsCompletedEntries(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.Register(EventSessionStart, func(Input) {}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < maxOnceCacheEntries+20; index++ {
		key := fmt.Sprintf("session-%d", index)
		if _, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, key, Input{}); err != nil {
			t.Fatal(err)
		}
	}
	dispatcher.onceMu.Lock()
	defer dispatcher.onceMu.Unlock()
	if len(dispatcher.once) > maxOnceCacheEntries {
		t.Fatalf("once cache entries = %d, limit %d", len(dispatcher.once), maxOnceCacheEntries)
	}
}

type oversizedHookError struct {
	message string
}

func (e *oversizedHookError) Error() string { return e.message }

func TestDispatcherBoundsAggregateFailureDiagnosticsAndPreservesCauses(t *testing.T) {
	const limit = 32
	dispatcher := New(Config{MaxOutputBytes: limit, FailurePolicy: FailureOpen})
	causes := []*oversizedHookError{
		{message: strings.Repeat("界", 64)},
		{message: strings.Repeat("错", 64)},
	}
	for _, cause := range causes {
		cause := cause
		if err := dispatcher.Register(EventPostToolUse, func(Input) error { return cause }); err != nil {
			t.Fatal(err)
		}
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != len(causes) {
		t.Fatalf("failures = %v", result.Failures)
	}
	total := 0
	for index, failure := range result.Failures {
		total += len(failure.Error())
		if !utf8.ValidString(failure.Error()) {
			t.Fatalf("failure %d is not valid UTF-8: %q", index, failure.Error())
		}
		if !errors.Is(failure, causes[index]) {
			t.Fatalf("failure %d lost original cause: %v", index, failure)
		}
		var typed *oversizedHookError
		if !errors.As(failure, &typed) || typed != causes[index] {
			t.Fatalf("failure %d lost typed cause: %v", index, failure)
		}
	}
	if total > limit {
		t.Fatalf("aggregate failure diagnostics = %d bytes, limit %d: %v", total, limit, result.Failures)
	}
}

func TestDispatcherBoundsFailClosedExecutionErrorAndPreservesCause(t *testing.T) {
	const limit = 32
	dispatcher := New(Config{MaxOutputBytes: limit, FailurePolicy: FailureClosed})
	cause := &oversizedHookError{message: strings.Repeat("界", 64)}
	if err := dispatcher.Register(EventPostToolUse, func(Input) error { return cause }); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err == nil {
		t.Fatal("dispatch unexpectedly succeeded")
	}
	if len(err.Error()) > limit || !utf8.ValidString(err.Error()) {
		t.Fatalf("execution error len=%d valid=%v: %q", len(err.Error()), utf8.ValidString(err.Error()), err.Error())
	}
	if len(result.Failures) != 1 || len(result.Failures[0].Error()) > limit || !utf8.ValidString(result.Failures[0].Error()) {
		t.Fatalf("result failures are not bounded: %v", result.Failures)
	}
	if !errors.Is(err, cause) || !errors.Is(result.Failures[0], cause) {
		t.Fatalf("original cause is not reachable: result=%v err=%v", result.Failures, err)
	}
	var execution *ExecutionError
	if !errors.As(err, &execution) || execution.Event != EventPostToolUse || execution.Index != 0 {
		t.Fatalf("execution metadata is not reachable: %#v", execution)
	}
}

func TestDispatcherBoundsPanicDiagnosticAndPreservesErrorCause(t *testing.T) {
	const limit = 32
	dispatcher := New(Config{MaxOutputBytes: limit, FailurePolicy: FailureOpen})
	cause := &oversizedHookError{message: strings.Repeat("惊", 64)}
	if err := dispatcher.Register(EventPostToolUse, func(Input) {
		panic(cause)
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failures) != 1 {
		t.Fatalf("failures = %v", result.Failures)
	}
	failure := result.Failures[0]
	if len(failure.Error()) > limit || !utf8.ValidString(failure.Error()) {
		t.Fatalf("panic diagnostic len=%d valid=%v: %q", len(failure.Error()), utf8.ValidString(failure.Error()), failure.Error())
	}
	if !errors.Is(failure, cause) {
		t.Fatalf("panic error cause is not reachable: %v", failure)
	}
}

func TestDispatcherSharesBudgetBetweenOutputAndJoinedFailure(t *testing.T) {
	const limit = 32
	dispatcher := New(Config{MaxOutputBytes: limit, FailurePolicy: FailureOpen})
	cause := &oversizedHookError{message: strings.Repeat("错", 64)}
	if err := dispatcher.Register(EventPostToolUse, func(Input) (Output, error) {
		return Output{Output: strings.Repeat("界", 64)}, cause
	}); err != nil {
		t.Fatal(err)
	}

	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || len(result.Failures) != 1 {
		t.Fatalf("result = %+v", result)
	}
	failure := result.Failures[0]
	if !errors.Is(failure, cause) || !errors.Is(failure, ErrOutputLimit) {
		t.Fatalf("joined causes are not reachable: %v", failure)
	}
	if !utf8.ValidString(failure.Error()) || !utf8.ValidString(result.Outputs[0].Output) {
		t.Fatalf("invalid UTF-8 result: %+v", result)
	}
	total := outputBudgetBytes(result.Outputs[0]) + len(failure.Error())
	if total > limit {
		t.Fatalf("aggregate diagnostics = %d bytes, limit %d: %+v", total, limit, result)
	}
}

func TestDispatchOncePropagatesParentTerminationAndRetries(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc, func())
		wantErr    error
	}{
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc, func()) {
				ctx, cancel := context.WithCancel(context.Background())
				return ctx, cancel, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc, func()) {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
				return ctx, cancel, func() {}
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := New(Config{Timeout: time.Second, FailurePolicy: FailureOpen})
			started := make(chan struct{})
			var calls atomic.Int32
			if err := dispatcher.Register(EventSessionStart, func(ctx context.Context, _ Input) error {
				if calls.Add(1) != 1 {
					return nil
				}
				close(started)
				<-ctx.Done()
				return ctx.Err()
			}); err != nil {
				t.Fatal(err)
			}

			ctx, cleanup, terminate := test.newContext()
			defer cleanup()
			type outcome struct {
				result Result
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := dispatcher.DispatchOnce(ctx, EventSessionStart, "session", Input{})
				done <- outcome{result: result, err: err}
			}()
			select {
			case <-started:
				terminate()
			case <-time.After(time.Second):
				t.Fatal("handler did not start")
			}

			first := <-done
			if !errors.Is(first.err, test.wantErr) || errors.Is(first.err, ErrHookTimeout) {
				t.Fatalf("first result=%+v err=%v, want parent error %v", first.result, first.err, test.wantErr)
			}
			second, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, "session", Input{})
			if err != nil || second.Skipped || calls.Load() != 2 {
				t.Fatalf("retry result=%+v err=%v calls=%d", second, err, calls.Load())
			}
		})
	}
}

func TestDispatchOnceDoesNotCacheFailOpenHandlerFailure(t *testing.T) {
	dispatcher := New(Config{FailurePolicy: FailureOpen})
	wantErr := errors.New("transient hook failure")
	var calls atomic.Int32
	if err := dispatcher.Register(EventSessionStart, func(Input) error {
		if calls.Add(1) == 1 {
			return wantErr
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	first, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, "session", Input{})
	if err != nil || !first.Failed() || !errors.Is(first.Failures[0], wantErr) {
		t.Fatalf("first result=%+v err=%v", first, err)
	}
	second, err := dispatcher.DispatchOnce(context.Background(), EventSessionStart, "session", Input{})
	if err != nil || second.Skipped || second.Failed() || calls.Load() != 2 {
		t.Fatalf("retry result=%+v err=%v calls=%d", second, err, calls.Load())
	}
}

func TestDispatcherPropagatesTerminatedParentWithoutHandlers(t *testing.T) {
	tests := []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				return ctx, cancel
			},
			wantErr: context.DeadlineExceeded,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := test.context()
			defer cancel()
			result, err := New(DefaultConfig()).Dispatch(ctx, EventStop, Input{})
			if !errors.Is(err, test.wantErr) || errors.Is(err, ErrHookTimeout) || result.Blocked {
				t.Fatalf("result=%+v err=%v, want parent error %v", result, err, test.wantErr)
			}
		})
	}
}
