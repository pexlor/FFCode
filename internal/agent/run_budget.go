package agent

import (
	"context"
	"fmt"
	"time"

	"MyCode/internal/llm"
)

const (
	defaultRunDuration     = 20 * time.Minute
	defaultRunInputTokens  = 2_000_000
	defaultRunOutputTokens = 128_000
	defaultRunToolCalls    = 512
	defaultProviderRetries = 2
)

// RunBudget bounds the resources consumed by one user turn. A zero field is
// unlimited when a caller supplies an explicit budget.
type RunBudget struct {
	MaxDuration        time.Duration
	MaxInputTokens     int64
	MaxOutputTokens    int64
	MaxToolCalls       int
	MaxProviderRetries int
}

func DefaultRunBudget() RunBudget {
	return RunBudget{
		MaxDuration:        defaultRunDuration,
		MaxInputTokens:     defaultRunInputTokens,
		MaxOutputTokens:    defaultRunOutputTokens,
		MaxToolCalls:       defaultRunToolCalls,
		MaxProviderRetries: defaultProviderRetries,
	}
}

func (b RunBudget) validate() error {
	if b.MaxDuration < 0 || b.MaxInputTokens < 0 || b.MaxOutputTokens < 0 || b.MaxToolCalls < 0 || b.MaxProviderRetries < 0 {
		return fmt.Errorf("run budget limits cannot be negative")
	}
	return nil
}

type budgetResource string

const (
	budgetInputTokens     budgetResource = "input_tokens"
	budgetOutputTokens    budgetResource = "output_tokens"
	budgetToolCalls       budgetResource = "tool_calls"
	budgetProviderRetries budgetResource = "provider_retries"
)

type budgetExceededError struct {
	resource budgetResource
	limit    int64
	used     int64
}

func (e *budgetExceededError) Error() string {
	return fmt.Sprintf("run budget exceeded for %s: used %d, limit %d", e.resource, e.used, e.limit)
}

type runBudgetState struct {
	budget          RunBudget
	usage           llm.UsageInfo
	toolCalls       int
	providerRetries int
	startedAt       time.Time
}

func newRunBudgetState(budget RunBudget) (*runBudgetState, error) {
	if err := budget.validate(); err != nil {
		return nil, err
	}
	return &runBudgetState{budget: budget, startedAt: time.Now()}, nil
}

func (s *runBudgetState) recordUsage(usage llm.UsageInfo) error {
	s.usage = addUsage(s.usage, usage)
	if limit := s.budget.MaxInputTokens; limit > 0 && s.usage.InputTokens > limit {
		return &budgetExceededError{resource: budgetInputTokens, limit: limit, used: s.usage.InputTokens}
	}
	if limit := s.budget.MaxOutputTokens; limit > 0 && s.usage.OutputTokens > limit {
		return &budgetExceededError{resource: budgetOutputTokens, limit: limit, used: s.usage.OutputTokens}
	}
	return nil
}

func (s *runBudgetState) reserveToolCalls(count int) error {
	used := s.toolCalls + count
	if limit := s.budget.MaxToolCalls; limit > 0 && used > limit {
		return &budgetExceededError{resource: budgetToolCalls, limit: int64(limit), used: int64(used)}
	}
	s.toolCalls = used
	return nil
}

func (s *runBudgetState) reserveProviderRetry() error {
	used := s.providerRetries + 1
	if limit := s.budget.MaxProviderRetries; limit > 0 && used > limit {
		return &budgetExceededError{resource: budgetProviderRetries, limit: int64(limit), used: int64(used)}
	}
	s.providerRetries = used
	return nil
}

func (s *runBudgetState) context(parent context.Context) (context.Context, context.CancelFunc) {
	if s.budget.MaxDuration > 0 {
		return context.WithTimeout(parent, s.budget.MaxDuration)
	}
	return context.WithCancel(parent)
}

type runBudgetSnapshot struct {
	Budget    RunBudget
	Usage     llm.UsageInfo
	ToolCalls int
	Elapsed   time.Duration
}

func (s *runBudgetState) snapshot(now time.Time) runBudgetSnapshot {
	return runBudgetSnapshot{Budget: s.budget, Usage: s.usage, ToolCalls: s.toolCalls, Elapsed: now.Sub(s.startedAt)}
}

func (s runBudgetSnapshot) softLimitReached(ratio float64) bool {
	if ratio <= 0 || ratio > 1 {
		return false
	}
	if s.Budget.MaxDuration > 0 && float64(s.Elapsed) >= float64(s.Budget.MaxDuration)*ratio {
		return true
	}
	if s.Budget.MaxInputTokens > 0 && float64(s.Usage.InputTokens) >= float64(s.Budget.MaxInputTokens)*ratio {
		return true
	}
	if s.Budget.MaxOutputTokens > 0 && float64(s.Usage.OutputTokens) >= float64(s.Budget.MaxOutputTokens)*ratio {
		return true
	}
	return s.Budget.MaxToolCalls > 0 && float64(s.ToolCalls) >= float64(s.Budget.MaxToolCalls)*ratio
}
