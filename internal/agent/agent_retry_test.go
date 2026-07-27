package agent

import (
	"context"
	"errors"
	"fmt"
	"testing"

	message "MyCode/internal/conversation"
	"MyCode/internal/llm"
)

type malformedThenSuccessClient struct {
	calls int
}

func (c *malformedThenSuccessClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.calls++
	events := make(chan llm.StreamEvent, 1)
	errs := make(chan error, 1)
	if c.calls == 1 {
		errs <- fmt.Errorf("%w: ReadFile", llm.ErrMalformedToolInput)
	} else {
		events <- llm.StreamEnd{StopReason: "end_turn"}
	}
	close(events)
	close(errs)
	return events, errs
}

func TestAgentRetriesMalformedToolInputOnce(t *testing.T) {
	client := &malformedThenSuccessClient{}
	agent, err := NewAgent(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	agent.MaxIterations = 1
	messages := &message.MessageManager{}
	messages.AddText("fix the issue")

	var done bool
	for event := range agent.Run(messages) {
		switch event := event.(type) {
		case TurnEndEvent:
			if event.Status != TurnCompleted {
				t.Fatalf("unexpected terminal event: %+v", event)
			}
			done = true
		}
	}

	if !done {
		t.Fatal("expected DoneEvent")
	}
	if client.calls != 2 {
		t.Fatalf("expected 2 stream attempts, got %d", client.calls)
	}
}

func TestAgentDoesNotRetryMalformedToolInputTwice(t *testing.T) {
	client := &alwaysMalformedClient{}
	agent, err := NewAgent(context.Background(), client, nil)
	if err != nil {
		t.Fatalf("NewAgent: %v", err)
	}
	messages := &message.MessageManager{}
	messages.AddText("fix the issue")

	var end TurnEndEvent
	for event := range agent.Run(messages) {
		if event, ok := event.(TurnEndEvent); ok {
			end = event
		}
	}

	if end.Status != TurnFailed || !errors.Is(end.Err, llm.ErrMalformedToolInput) {
		t.Fatalf("expected malformed tool input failure, got %+v", end)
	}
	if client.calls != 2 {
		t.Fatalf("expected retry limit of 1, got %d attempts", client.calls)
	}
}

type alwaysMalformedClient struct {
	calls int
}

func (c *alwaysMalformedClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	c.calls++
	events := make(chan llm.StreamEvent)
	errs := make(chan error, 1)
	errs <- fmt.Errorf("%w: ReadFile", llm.ErrMalformedToolInput)
	close(events)
	close(errs)
	return events, errs
}
