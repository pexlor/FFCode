package agent

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"FFCode/internal/llm"
	"FFCode/internal/tool"
)

type retryResponse struct {
	events []llm.StreamEvent
	err    error
}

type retryClient struct {
	mu        sync.Mutex
	responses []retryResponse
	calls     int
}

func (c *retryClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.mu.Lock()
	response := c.responses[c.calls]
	c.calls++
	c.mu.Unlock()
	events := make(chan llm.StreamEvent, len(response.events))
	errorsChannel := make(chan error, 1)
	for _, event := range response.events {
		events <- event
	}
	if response.err != nil {
		errorsChannel <- response.err
	}
	close(events)
	close(errorsChannel)
	return events, errorsChannel
}

func noDelayRetryPolicy(maxRetries int) llm.RetryPolicy {
	return llm.RetryPolicy{
		MaxRetries: maxRetries,
		BaseDelay:  time.Nanosecond,
		MaxDelay:   time.Nanosecond,
		Jitter:     func(time.Duration) time.Duration { return 0 },
	}
}

func TestAgentRetriesTransientProviderErrorWithoutLeakingFailedAttempt(t *testing.T) {
	client := &retryClient{responses: []retryResponse{
		{events: []llm.StreamEvent{llm.TextStream{Text: "discarded"}}, err: &llm.ProviderError{Provider: "test", StatusCode: 529, Retryable: true, Err: errors.New("busy")}},
		{events: []llm.StreamEvent{llm.TextStream{Text: "kept"}, llm.StreamEnd{StopReason: "end_turn"}}},
	}}
	runner, err := NewAgent(context.Background(), client, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.ProviderRetryPolicy = noDelayRetryPolicy(2)

	var text string
	var retries int
	var terminal TurnEndEvent
	for event := range runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxProviderRetries: 2}) {
		switch item := event.(type) {
		case TextEvent:
			text += item.Text
		case ProviderRetryEvent:
			retries++
		case TurnEndEvent:
			terminal = item
		}
	}
	if text != "kept" || retries != 1 || terminal.Status != TurnCompleted {
		t.Fatalf("text = %q, retries = %d, terminal = %+v", text, retries, terminal)
	}
	if client.calls != 2 {
		t.Fatalf("provider calls = %d", client.calls)
	}
}

func TestAgentDoesNotRetryNonRetryableProviderError(t *testing.T) {
	client := &retryClient{responses: []retryResponse{{err: &llm.ProviderError{Provider: "test", StatusCode: 401, Err: errors.New("unauthorized")}}}}
	runner, err := NewAgent(context.Background(), client, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.ProviderRetryPolicy = noDelayRetryPolicy(2)

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxProviderRetries: 2}))
	if client.calls != 1 || terminal.StopReason != StopProviderError {
		t.Fatalf("calls = %d, terminal = %+v", client.calls, terminal)
	}
}

func TestAgentStopsWhenProviderRetryBudgetIsExhausted(t *testing.T) {
	retryable := &llm.ProviderError{Provider: "test", StatusCode: 503, Retryable: true, Err: errors.New("unavailable")}
	client := &retryClient{responses: []retryResponse{{err: retryable}, {err: retryable}}}
	runner, err := NewAgent(context.Background(), client, tool.NewToolsManager())
	if err != nil {
		t.Fatal(err)
	}
	runner.ProviderRetryPolicy = noDelayRetryPolicy(3)

	terminal := terminalEvent(runner.RunContextWithBudget(context.Background(), testSession(), RunBudget{MaxProviderRetries: 1}))
	if client.calls != 2 || terminal.StopReason != StopBudgetExceeded {
		t.Fatalf("calls = %d, terminal = %+v", client.calls, terminal)
	}
}
