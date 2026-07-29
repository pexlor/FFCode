package jsonl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"MyCode/internal/agent"
	"MyCode/internal/conversation"
)

type fakeSessions struct {
	current  *conversation.Session
	messages []string
}

func newFakeSessions() *fakeSessions {
	return &fakeSessions{current: &conversation.Session{ID: "session-1", SystemPrompt: "system"}}
}

func (s *fakeSessions) AddUserMessage(_ context.Context, content string) error {
	s.messages = append(s.messages, content)
	s.current.AddText(content)
	return nil
}

func (s *fakeSessions) Current() *conversation.Session { return s.current }

type fakeTurnRunner struct {
	responses [][]agent.AgentEvent
	calls     int
}

func (r *fakeTurnRunner) RunContext(_ context.Context, _ *conversation.Session) <-chan agent.AgentEvent {
	ch := make(chan agent.AgentEvent, 16)
	response := r.responses[r.calls]
	r.calls++
	for _, event := range response {
		ch <- event
	}
	close(ch)
	return ch
}

func TestRunProcessesEachNonEmptyInputLineAsOneTurn(t *testing.T) {
	sessions := newFakeSessions()
	runner := &fakeTurnRunner{responses: [][]agent.AgentEvent{
		{agent.TextEvent{Text: "one"}, agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn}},
		{agent.TextEvent{Text: "two"}, agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn}},
	}}
	var output bytes.Buffer

	err := Run(context.Background(), Runtime{
		In: strings.NewReader("first\n\nsecond\n"), Out: &output,
		Runner: runner, Sessions: sessions,
	})
	if err != nil {
		t.Fatal(err)
	}

	events := decodeLines(t, output.String())
	if len(events) != 6 {
		t.Fatalf("got %d events: %s", len(events), output.String())
	}
	wantTypes := []string{"turn_started", "text_delta", "turn_finished", "turn_started", "text_delta", "turn_finished"}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, events[index].Type, want)
		}
	}
	if events[0].TurnID != "turn-1" || events[3].TurnID != "turn-2" {
		t.Fatalf("turn ids = %q, %q", events[0].TurnID, events[3].TurnID)
	}
	if strings.Join(sessions.messages, ",") != "first,second" {
		t.Fatalf("messages = %#v", sessions.messages)
	}
}

func TestRunContinuesAfterFailedTurn(t *testing.T) {
	sessions := newFakeSessions()
	runner := &fakeTurnRunner{responses: [][]agent.AgentEvent{
		{agent.TurnEndEvent{Status: agent.TurnFailed, StopReason: agent.StopAgentError}},
		{agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn}},
	}}
	var output bytes.Buffer

	if err := Run(context.Background(), Runtime{
		In: strings.NewReader("first\nsecond\n"), Out: &output,
		Runner: runner, Sessions: sessions,
	}); err != nil {
		t.Fatal(err)
	}

	events := decodeLines(t, output.String())
	if len(events) != 4 || events[1].Data["status"] != "failed" || events[3].Data["status"] != "completed" {
		t.Fatalf("events = %#v", events)
	}
}

func TestRunEncodesQualityWarningBeforeCompletedTerminalEvent(t *testing.T) {
	sessions := newFakeSessions()
	runner := &fakeTurnRunner{responses: [][]agent.AgentEvent{{
		agent.QualityWarningEvent{
			Code: "QG001", Severity: agent.WarningSeverityWarning,
			Message: "source changes were not verified", Evidence: []string{"internal/agent/agent.go"},
		},
		agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn},
	}}}
	var output bytes.Buffer

	if err := Run(context.Background(), Runtime{
		In: strings.NewReader("request\n"), Out: &output,
		Runner: runner, Sessions: sessions,
	}); err != nil {
		t.Fatal(err)
	}

	events := decodeLines(t, output.String())
	wantTypes := []string{"turn_started", "quality_warning", "turn_finished"}
	if len(events) != len(wantTypes) {
		t.Fatalf("events = %#v", events)
	}
	for index, want := range wantTypes {
		if events[index].Type != want {
			t.Fatalf("event %d type = %q, want %q", index, events[index].Type, want)
		}
	}
	if events[2].Data["status"] != "completed" || events[2].Data["stop_reason"] != "end_turn" {
		t.Fatalf("terminal data = %#v", events[2].Data)
	}
}

func TestRunSynthesizesFailureWhenAgentClosesWithoutTerminalEvent(t *testing.T) {
	sessions := newFakeSessions()
	runner := &fakeTurnRunner{responses: [][]agent.AgentEvent{{agent.TextEvent{Text: "partial"}}}}
	var output bytes.Buffer

	if err := Run(context.Background(), Runtime{
		In: strings.NewReader("request\n"), Out: &output,
		Runner: runner, Sessions: sessions,
	}); err != nil {
		t.Fatal(err)
	}

	events := decodeLines(t, output.String())
	last := events[len(events)-1]
	if last.Type != "turn_finished" || last.Data["status"] != "failed" || last.Data["stop_reason"] != "agent_error" {
		t.Fatalf("last event = %#v", last)
	}
	errorData, ok := last.Data["error"].(map[string]any)
	if !ok || !strings.Contains(errorData["message"].(string), "without terminal event") {
		t.Fatalf("error = %#v", last.Data["error"])
	}
}

type decodedEvent struct {
	Sequence  uint64         `json:"sequence"`
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	TurnID    string         `json:"turn_id"`
	Data      map[string]any `json:"data"`
}

func decodeLines(t *testing.T, output string) []decodedEvent {
	t.Helper()
	var result []decodedEvent
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		var event decodedEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode %q: %v", line, err)
		}
		result = append(result, event)
	}
	return result
}
