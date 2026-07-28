package agent

import (
	"context"
	"errors"
	"sync"
	"time"

	"MyCode/internal/llm"
)

var ErrChildBudgetUnavailable = errors.New("child run budget is unavailable")

type childRuntimeKey struct{}
type agentEventSinkKey struct{}

type childRuntime struct {
	budget *runBudgetState
}

// ChildBudgetReservation owns a portion of the parent run budget until it is
// committed or released. Completion methods are idempotent.
type ChildBudgetReservation struct {
	Budget RunBudget

	state *runBudgetState
	once  sync.Once
}

func withChildRuntime(ctx context.Context, state *runBudgetState) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, childRuntimeKey{}, &childRuntime{budget: state})
}

// NewChildRuntimeContext creates a standalone parent runtime context for
// non-interactive runners and tests that host delegating tools outside Agent.Run.
func NewChildRuntimeContext(ctx context.Context, budget RunBudget) (context.Context, error) {
	state, err := newRunBudgetState(budget)
	if err != nil {
		return nil, err
	}
	return withChildRuntime(ctx, state), nil
}

// ReserveChildBudget atomically clamps a child request to the unreserved
// remainder of its parent run.
func ReserveChildBudget(ctx context.Context, requested RunBudget) (*ChildBudgetReservation, error) {
	runtime, _ := ctx.Value(childRuntimeKey{}).(*childRuntime)
	if runtime == nil || runtime.budget == nil {
		return nil, ErrChildBudgetUnavailable
	}
	state := runtime.budget
	state.mu.Lock()
	defer state.mu.Unlock()

	effective := requested
	effective.MaxDuration = remainingDuration(state, requested.MaxDuration, time.Now())
	effective.MaxInputTokens = clampInt64(requested.MaxInputTokens, state.budget.MaxInputTokens, state.usage.InputTokens+state.reservedInput)
	effective.MaxOutputTokens = clampInt64(requested.MaxOutputTokens, state.budget.MaxOutputTokens, state.usage.OutputTokens+state.reservedOutput)
	effective.MaxToolCalls = clampInt(requested.MaxToolCalls, state.budget.MaxToolCalls, state.toolCalls+state.reservedTools)
	if exhaustedRequested(requested, effective) {
		return nil, ErrChildBudgetUnavailable
	}
	state.reservedInput += effective.MaxInputTokens
	state.reservedOutput += effective.MaxOutputTokens
	state.reservedTools += effective.MaxToolCalls
	return &ChildBudgetReservation{Budget: effective, state: state}, nil
}

func (r *ChildBudgetReservation) Commit(usage llm.UsageInfo, toolCalls int) {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.state.mu.Lock()
		defer r.state.mu.Unlock()
		r.releaseLocked()
		_ = r.state.recordUsageLocked(usage)
		_ = r.state.reserveToolCallsLocked(toolCalls)
	})
}

func (r *ChildBudgetReservation) Release() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.state.mu.Lock()
		defer r.state.mu.Unlock()
		r.releaseLocked()
	})
}

func (r *ChildBudgetReservation) releaseLocked() {
	r.state.reservedInput -= r.Budget.MaxInputTokens
	r.state.reservedOutput -= r.Budget.MaxOutputTokens
	r.state.reservedTools -= r.Budget.MaxToolCalls
}

func ClaimSubagentCall(ctx context.Context, limit int) bool {
	runtime, _ := ctx.Value(childRuntimeKey{}).(*childRuntime)
	if runtime == nil || runtime.budget == nil || limit <= 0 {
		return false
	}
	state := runtime.budget
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.subagentCalls >= limit {
		return false
	}
	state.subagentCalls++
	return true
}

func withAgentEventSink(ctx context.Context, sink func(AgentEvent) bool) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, agentEventSinkKey{}, sink)
}

// WithAgentEventSink lets an embedding runner observe nested Agent events.
func WithAgentEventSink(ctx context.Context, sink func(AgentEvent) bool) context.Context {
	return withAgentEventSink(ctx, sink)
}

// EmitAgentEvent forwards a nested runtime event to the parent Agent stream.
func EmitAgentEvent(ctx context.Context, event AgentEvent) bool {
	if ctx == nil {
		return false
	}
	sink, _ := ctx.Value(agentEventSinkKey{}).(func(AgentEvent) bool)
	return sink != nil && sink(event)
}

func remainingDuration(state *runBudgetState, requested time.Duration, now time.Time) time.Duration {
	if state.budget.MaxDuration == 0 {
		return requested
	}
	remaining := state.budget.MaxDuration - now.Sub(state.startedAt)
	if remaining < 0 {
		remaining = 0
	}
	if requested == 0 || requested > remaining {
		return remaining
	}
	return requested
}

func clampInt64(requested, limit, used int64) int64 {
	if limit == 0 {
		return requested
	}
	remaining := limit - used
	if remaining <= 0 {
		return 0
	}
	if requested == 0 || requested > remaining {
		return remaining
	}
	return requested
}

func clampInt(requested, limit, used int) int {
	if limit == 0 {
		return requested
	}
	remaining := limit - used
	if remaining <= 0 {
		return 0
	}
	if requested == 0 || requested > remaining {
		return remaining
	}
	return requested
}

func exhaustedRequested(requested, effective RunBudget) bool {
	return requested.MaxDuration > 0 && effective.MaxDuration <= 0 ||
		requested.MaxInputTokens > 0 && effective.MaxInputTokens <= 0 ||
		requested.MaxOutputTokens > 0 && effective.MaxOutputTokens <= 0 ||
		requested.MaxToolCalls > 0 && effective.MaxToolCalls <= 0
}
