package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	message "MyCode/internal/conversation"
	"MyCode/internal/llm"
)

type finalReasonClient struct {
	reason string
	err    error
	tools  bool
}

func (c finalReasonClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	events := make(chan llm.StreamEvent, 2)
	errs := make(chan error, 1)
	if c.tools {
		events <- llm.ToolCallComplete{ToolID: "tool-1", ToolName: "missing", Arguments: map[string]any{}}
	}
	if c.err != nil {
		errs <- c.err
	} else {
		events <- llm.StreamEnd{StopReason: c.reason}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestAgentStopReasonsProduceOneTurnEndEvent(t *testing.T) {
	tests := []struct {
		name       string
		reason     string
		status     TurnStatus
		stopReason StopReason
	}{
		{name: "anthropic end turn", reason: "end_turn", status: TurnCompleted, stopReason: StopEndTurn},
		{name: "openai stop", reason: "stop", status: TurnCompleted, stopReason: StopEndTurn},
		{name: "stop sequence", reason: "stop_sequence", status: TurnCompleted, stopReason: StopEndTurn},
		{name: "anthropic token limit", reason: "max_tokens", status: TurnIncomplete, stopReason: StopMaxTokens},
		{name: "openai token limit", reason: "length", status: TurnIncomplete, stopReason: StopMaxTokens},
		{name: "unknown reason", reason: "future_reason", status: TurnIncomplete, stopReason: StopAgentError},
		{name: "missing reason", reason: "", status: TurnIncomplete, stopReason: StopAgentError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := NewAgent(context.Background(), finalReasonClient{reason: test.reason}, nil)
			if err != nil {
				t.Fatal(err)
			}

			end := collectTurnEnd(t, runner.Run(&message.MessageManager{}))
			if end.Status != test.status || end.StopReason != test.stopReason {
				t.Fatalf("turn end = %+v, want status=%s reason=%s", end, test.status, test.stopReason)
			}
			if end.ProviderReason != test.reason {
				t.Fatalf("provider reason = %q, want %q", end.ProviderReason, test.reason)
			}
		})
	}
}

func TestAgentErrorsProduceOneTurnEndEvent(t *testing.T) {
	streamFailure := errors.New("stream failed")
	tests := []struct {
		name       string
		ctx        func() context.Context
		client     llm.LLMClient
		configure  func(*Agent)
		status     TurnStatus
		stopReason StopReason
		err        error
	}{
		{
			name: "stream error", ctx: context.Background,
			client: finalReasonClient{err: streamFailure},
			status: TurnFailed, stopReason: StopAgentError, err: streamFailure,
		},
		{
			name: "cancelled", ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			client: finalReasonClient{}, status: TurnCancelled, stopReason: StopCancelled, err: context.Canceled,
		},
		{
			name: "deadline", ctx: func() context.Context {
				ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)
				return ctx
			},
			client: finalReasonClient{}, status: TurnFailed, stopReason: StopDeadlineExceeded, err: context.DeadlineExceeded,
		},
		{
			name: "invalid agent", ctx: context.Background,
			client: finalReasonClient{}, configure: func(agent *Agent) { agent.MaxIterations = 0 },
			status: TurnFailed, stopReason: StopAgentError,
		},
		{
			name: "iteration exhausted", ctx: context.Background,
			client: finalReasonClient{reason: "tool_use", tools: true}, configure: func(agent *Agent) { agent.MaxIterations = 1 },
			status: TurnFailed, stopReason: StopAgentError,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := NewAgent(context.Background(), test.client, nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.configure != nil {
				test.configure(runner)
			}

			end := collectTurnEnd(t, runner.RunContext(test.ctx(), &message.MessageManager{}))
			if end.Status != test.status || end.StopReason != test.stopReason {
				t.Fatalf("turn end = %+v, want status=%s reason=%s", end, test.status, test.stopReason)
			}
			if test.err != nil && !errors.Is(end.Err, test.err) {
				t.Fatalf("turn error = %v, want %v", end.Err, test.err)
			}
		})
	}
}

func collectTurnEnd(t *testing.T, events <-chan AgentEvent) TurnEndEvent {
	t.Helper()
	var ends []TurnEndEvent
	for event := range events {
		if end, ok := event.(TurnEndEvent); ok {
			ends = append(ends, end)
		}
	}
	if len(ends) != 1 {
		t.Fatalf("got %d terminal events, want 1", len(ends))
	}
	return ends[0]
}
