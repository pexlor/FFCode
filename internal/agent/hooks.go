package agent

import (
	contextmanager "MyCode/internal/context"
	"context"
	"errors"
	"fmt"
	"strings"

	"MyCode/internal/conversation"
	"MyCode/internal/hook"
)

var (
	ErrHookRejected         = errors.New("agent lifecycle rejected by hook")
	ErrSubagentHookRejected = errors.New("subagent lifecycle rejected by hook")
)

func (a *Agent) dispatchRunStartHooks(ctx context.Context, conversationContext *contextmanager.ConversationContext) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if a == nil || a.Hooks == nil || conversationContext == nil {
		return ctx, nil
	}
	base, _ := hook.InputFromContext(ctx)
	base.SessionID = conversationContext.SessionID
	base.Workspace = conversationContext.Workspace
	sessionInput := base
	sessionInput.Reason = "agent_run"
	lifecycleKey := conversationContext.LifecycleKey
	result, err := a.Hooks.DispatchOnce(ctx, hook.EventSessionStart, lifecycleKey, sessionInput)
	if err != nil {
		return ctx, fmt.Errorf("session_start hook: %w", err)
	}
	if result.Blocked {
		a.Hooks.ResetOnce(hook.EventSessionStart, lifecycleKey)
		return ctx, fmt.Errorf("%w: session_start: %s", ErrHookRejected, lifecycleHookReason(result.Reason))
	}

	index, ordinal := latestUserMessage(conversationContext.History)
	if index < 0 {
		return hook.WithInput(ctx, base), nil
	}
	prompt := conversationContext.History[index].Content
	promptInput := base
	promptInput.UserPrompt = prompt
	promptInput.Prompt = prompt
	promptKey := fmt.Sprintf("%s:%d", conversationContext.SessionID, ordinal)
	result, err = a.Hooks.DispatchOnce(
		hook.WithInput(ctx, promptInput),
		hook.EventUserPromptSubmit,
		promptKey,
		promptInput,
	)
	if err != nil {
		return ctx, fmt.Errorf("user_prompt_submit hook: %w", err)
	}
	if result.Blocked {
		a.Hooks.ResetOnce(hook.EventUserPromptSubmit, promptKey)
		return ctx, fmt.Errorf("%w: user_prompt_submit: %s", ErrHookRejected, lifecycleHookReason(result.Reason))
	}
	for _, key := range []string{"user_prompt", "prompt"} {
		if updated, ok := result.UpdatedInput[key].(string); ok {
			conversationContext.History[index].Content = updated
			prompt = updated
			break
		}
	}
	base.UserPrompt = prompt
	base.Prompt = prompt
	return hook.WithInput(ctx, base), nil
}

func (a *Agent) dispatchStopHook(ctx context.Context, conversationContext *contextmanager.ConversationContext, event TurnEndEvent) TurnEndEvent {
	if a == nil || a.Hooks == nil {
		return event
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithoutCancel(ctx)
	input, _ := hook.InputFromContext(ctx)
	if conversationContext != nil {
		input.SessionID = conversationContext.SessionID
		input.Workspace = conversationContext.Workspace
	}
	input.Reason = string(event.StopReason)
	input.Metadata = mergeLifecycleMetadata(input.Metadata, map[string]any{
		"status":          string(event.Status),
		"stop_reason":     string(event.StopReason),
		"provider_reason": event.ProviderReason,
		"usage": map[string]any{
			"input_tokens":          event.Usage.InputTokens,
			"output_tokens":         event.Usage.OutputTokens,
			"total_tokens":          event.Usage.TotalTokens,
			"cache_read_tokens":     event.Usage.CacheReadTokens,
			"cache_creation_tokens": event.Usage.CacheCreationTokens,
		},
	})
	if event.Err != nil {
		input.Metadata["error"] = event.Err.Error()
	}
	result, err := a.Hooks.Dispatch(hook.WithInput(ctx, input), hook.EventStop, input)
	if err == nil && !result.Blocked {
		return event
	}
	if err == nil {
		err = fmt.Errorf("%w: stop: %s", ErrHookRejected, lifecycleHookReason(result.Reason))
	} else {
		err = fmt.Errorf("stop hook: %w", err)
	}
	if event.Err != nil {
		event.Err = errors.Join(event.Err, err)
	} else {
		event.Err = err
	}
	event.Status = TurnFailed
	event.StopReason = StopAgentError
	return event
}

// RunSubagent brackets a caller-provided subagent operation with paired
// subagent_start/subagent_stop hooks. The stop hook always runs, including on
// cancellation, returned errors, and panics.
func (a *Agent) RunSubagent(ctx context.Context, input hook.Input, run func(context.Context) error) (err error) {
	if run == nil {
		return errors.New("subagent run function is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	base, _ := hook.InputFromContext(ctx)
	input = mergeLifecycleInput(base, input)
	runContext := hook.WithInput(ctx, input)
	if err := a.NotifySubagentStart(runContext, input); err != nil {
		return err
	}
	defer func() {
		recovered := recover()
		stopInput := input
		if recovered != nil {
			stopInput.Reason = fmt.Sprintf("panic: %v", recovered)
			stopInput.IsError = true
		} else if err != nil {
			stopInput.Reason = err.Error()
			stopInput.IsError = true
		}
		stopErr := a.NotifySubagentStop(context.WithoutCancel(runContext), stopInput)
		if recovered != nil {
			if stopErr != nil {
				panic(errors.Join(panicAsError(recovered), stopErr))
			}
			panic(recovered)
		}
		if stopErr != nil {
			err = errors.Join(err, stopErr)
		}
	}()
	return run(runContext)
}

func panicAsError(recovered any) error {
	if err, ok := recovered.(error); ok {
		return err
	}
	return fmt.Errorf("panic: %v", recovered)
}

func (a *Agent) NotifySubagentStart(ctx context.Context, input hook.Input) error {
	return a.dispatchSubagentHook(ctx, hook.EventSubagentStart, input)
}

func (a *Agent) NotifySubagentStop(ctx context.Context, input hook.Input) error {
	return a.dispatchSubagentHook(ctx, hook.EventSubagentStop, input)
}

func (a *Agent) SubagentStart(ctx context.Context, input hook.Input) error {
	return a.NotifySubagentStart(ctx, input)
}

func (a *Agent) SubagentStop(ctx context.Context, input hook.Input) error {
	return a.NotifySubagentStop(ctx, input)
}

func (a *Agent) dispatchSubagentHook(ctx context.Context, event hook.Event, input hook.Input) error {
	if a == nil || a.Hooks == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	result, err := a.Hooks.Dispatch(hook.WithInput(ctx, input), event, input)
	if err != nil {
		return fmt.Errorf("%s hook: %w", event, err)
	}
	if result.Blocked {
		return fmt.Errorf("%w: %s: %s", ErrSubagentHookRejected, event, lifecycleHookReason(result.Reason))
	}
	return nil
}

func latestUserMessage(history []conversation.Message) (index int, ordinal int) {
	index = -1
	for messageIndex, message := range history {
		if message.Role != conversation.USER {
			continue
		}
		ordinal++
		index = messageIndex
	}
	return index, ordinal
}

func lifecycleHookReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "hook denied operation"
	}
	return reason
}

func mergeLifecycleInput(base, override hook.Input) hook.Input {
	if override.SessionID == "" {
		override.SessionID = base.SessionID
	}
	if override.Workspace == "" {
		override.Workspace = base.Workspace
	}
	if override.UserPrompt == "" {
		override.UserPrompt = base.UserPrompt
	}
	if override.Prompt == "" {
		override.Prompt = base.Prompt
	}
	override.Metadata = mergeLifecycleMetadata(base.Metadata, override.Metadata)
	return override
}

func mergeLifecycleMetadata(base, override map[string]any) map[string]any {
	result := make(map[string]any, len(base)+len(override))
	for key, value := range base {
		result[key] = value
	}
	for key, value := range override {
		result[key] = value
	}
	return result
}
