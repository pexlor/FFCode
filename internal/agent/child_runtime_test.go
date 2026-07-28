package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"MyCode/internal/llm"
)

func TestChildBudgetReservationsDoNotOverspendParent(t *testing.T) {
	state, err := newRunBudgetState(RunBudget{
		MaxDuration:     time.Minute,
		MaxInputTokens:  10,
		MaxOutputTokens: 10,
		MaxToolCalls:    5,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withChildRuntime(context.Background(), state)

	first, err := ReserveChildBudget(ctx, RunBudget{MaxDuration: time.Minute, MaxInputTokens: 6, MaxOutputTokens: 4, MaxToolCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	second, err := ReserveChildBudget(ctx, RunBudget{MaxDuration: time.Minute, MaxInputTokens: 6, MaxOutputTokens: 8, MaxToolCalls: 3})
	if err != nil {
		t.Fatal(err)
	}
	if first.Budget.MaxInputTokens != 6 || first.Budget.MaxOutputTokens != 4 || first.Budget.MaxToolCalls != 3 {
		t.Fatalf("first budget = %+v", first.Budget)
	}
	if second.Budget.MaxInputTokens != 4 || second.Budget.MaxOutputTokens != 6 || second.Budget.MaxToolCalls != 2 {
		t.Fatalf("second budget = %+v", second.Budget)
	}
	if _, err := ReserveChildBudget(ctx, RunBudget{MaxInputTokens: 1, MaxOutputTokens: 1, MaxToolCalls: 1}); err == nil {
		t.Fatal("expected exhausted parent reservation to fail")
	}

	first.Commit(llm.UsageInfo{InputTokens: 3, OutputTokens: 2, TotalTokens: 5}, 1)
	second.Release()
	snapshot := state.snapshot(time.Now())
	if snapshot.Usage.InputTokens != 3 || snapshot.Usage.OutputTokens != 2 || snapshot.ToolCalls != 1 {
		t.Fatalf("parent snapshot = %+v", snapshot)
	}
}

func TestChildBudgetConcurrentReservationsStayWithinLimit(t *testing.T) {
	state, err := newRunBudgetState(RunBudget{MaxInputTokens: 8, MaxOutputTokens: 8, MaxToolCalls: 8})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withChildRuntime(context.Background(), state)
	var wg sync.WaitGroup
	budgets := make(chan RunBudget, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reservation, reserveErr := ReserveChildBudget(ctx, RunBudget{MaxInputTokens: 6, MaxOutputTokens: 6, MaxToolCalls: 6})
			if reserveErr != nil {
				return
			}
			budgets <- reservation.Budget
		}()
	}
	wg.Wait()
	close(budgets)
	var input, output, calls int64
	for budget := range budgets {
		input += budget.MaxInputTokens
		output += budget.MaxOutputTokens
		calls += int64(budget.MaxToolCalls)
	}
	if input > 8 || output > 8 || calls > 8 {
		t.Fatalf("reservations exceeded parent: input=%d output=%d calls=%d", input, output, calls)
	}
}

func TestClaimSubagentCallIsBoundToOneRunContext(t *testing.T) {
	state, err := newRunBudgetState(RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := withChildRuntime(context.Background(), state)
	if !ClaimSubagentCall(ctx, 2) || !ClaimSubagentCall(ctx, 2) || ClaimSubagentCall(ctx, 2) {
		t.Fatal("subagent call limit was not enforced")
	}

	otherState, err := newRunBudgetState(RunBudget{})
	if err != nil {
		t.Fatal(err)
	}
	if !ClaimSubagentCall(withChildRuntime(context.Background(), otherState), 2) {
		t.Fatal("a different run should have an independent call counter")
	}
}

func TestEmitAgentEventUsesContextSink(t *testing.T) {
	events := make(chan AgentEvent, 1)
	ctx := withAgentEventSink(context.Background(), func(event AgentEvent) bool {
		events <- event
		return true
	})
	want := SubagentStartEvent{SubagentID: "child-1", ParentSessionID: "parent-1", Task: "inspect"}
	if !EmitAgentEvent(ctx, want) {
		t.Fatal("event was not accepted")
	}
	if got := <-events; got != want {
		t.Fatalf("event = %#v, want %#v", got, want)
	}
}
