package terminal

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"

	"FFCode/internal/agent"
)

var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestAssistantLabelAndFirstTextRenderOnSameLine(t *testing.T) {
	var status bytes.Buffer
	var output bytes.Buffer
	renderer := newAgentEventRenderer(&status, &output)

	if err := renderer.render(agent.TextEvent{Text: "模型回复"}); err != nil {
		t.Fatalf("render text: %v", err)
	}
	if err := renderer.render(agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn}); err != nil {
		t.Fatalf("finish render: %v", err)
	}

	plain := ansiSequence.ReplaceAllString(output.String(), "")
	firstLine := strings.SplitN(plain, "\n", 2)[0]
	if !strings.Contains(firstLine, "● 模型回复") {
		t.Fatalf("assistant label and text should share the first line; got %q", plain)
	}
}

func TestTurnEndEventRendersStableCompletionStatus(t *testing.T) {
	var status bytes.Buffer
	renderer := newAgentEventRenderer(&status, &bytes.Buffer{})

	if err := renderer.render(agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn}); err != nil {
		t.Fatal(err)
	}
	plain := ansiSequence.ReplaceAllString(status.String(), "")
	if !strings.Contains(plain, "done: end_turn") {
		t.Fatalf("status = %q", plain)
	}
}

func TestTurnEndEventReturnsTerminalFailure(t *testing.T) {
	want := errors.New("stream failed")
	renderer := newAgentEventRenderer(&bytes.Buffer{}, &bytes.Buffer{})

	err := renderer.render(agent.TurnEndEvent{
		Status: agent.TurnFailed, StopReason: agent.StopAgentError, Err: want,
	})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestQualityWarningRendersWithoutFailingTurn(t *testing.T) {
	var status bytes.Buffer
	renderer := newAgentEventRenderer(&status, &bytes.Buffer{})

	if err := renderer.render(agent.QualityWarningEvent{
		Code: "QG001", Severity: agent.WarningSeverityWarning,
		Message:  "source changes were not verified",
		Evidence: []string{"internal/agent/agent.go"},
	}); err != nil {
		t.Fatal(err)
	}
	plain := ansiSequence.ReplaceAllString(status.String(), "")
	if !strings.Contains(plain, "Quality warning QG001:") ||
		!strings.Contains(plain, "source changes were not verified") ||
		!strings.Contains(plain, "internal/agent/agent.go") {
		t.Fatalf("status = %q", plain)
	}
}

func TestSubagentEventsRenderCompactLifecycleOnly(t *testing.T) {
	var status bytes.Buffer
	renderer := newAgentEventRenderer(&status, &bytes.Buffer{})
	if err := renderer.render(agent.SubagentStartEvent{SubagentID: "child-1", Task: "inspect permissions"}); err != nil {
		t.Fatal(err)
	}
	if err := renderer.render(agent.SubagentEvent{SubagentID: "child-1", Event: agent.TurnEndEvent{
		Status: agent.TurnFailed, StopReason: agent.StopAgentError, Err: errors.New("child failed"),
	}}); err != nil {
		t.Fatalf("wrapped child failure must not fail the parent renderer: %v", err)
	}
	if err := renderer.render(agent.SubagentStopEvent{SubagentID: "child-1", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	plain := ansiSequence.ReplaceAllString(status.String(), "")
	if !strings.Contains(plain, "inspect permissions") || !strings.Contains(plain, "completed") {
		t.Fatalf("status = %q", plain)
	}
}

func TestConsumeAgentEventsDrainsStreamAfterInterrupt(t *testing.T) {
	events := make(chan agent.AgentEvent, 64)
	interrupts := make(chan os.Signal, 1)
	cancelled := make(chan struct{})
	interrupts <- os.Interrupt
	go func() {
		<-cancelled
		for index := 0; index < 40; index++ {
			events <- agent.TextEvent{Text: "pending"}
		}
		events <- agent.TurnEndEvent{Status: agent.TurnCancelled, StopReason: agent.StopCancelled}
		close(events)
	}()

	interrupted, failed := consumeAgentEvents(events, interrupts, func() { close(cancelled) }, newAgentEventRenderer(&bytes.Buffer{}, &bytes.Buffer{}))
	if !interrupted || failed {
		t.Fatalf("interrupted=%v failed=%v", interrupted, failed)
	}
	if len(events) != 0 {
		t.Fatalf("event stream was not drained: %d buffered events", len(events))
	}
}
