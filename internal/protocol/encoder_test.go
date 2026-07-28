package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"MyCode/internal/agent"
	"MyCode/internal/llm"
)

func TestEncoderMapsAgentEventsToVersionOneProtocol(t *testing.T) {
	tests := []struct {
		name      string
		event     agent.AgentEvent
		eventType string
		assert    func(*testing.T, map[string]any)
	}{
		{name: "thinking started", event: agent.ThinkingStartEvent{}, eventType: "thinking_started"},
		{name: "run phase", event: agent.RunPhaseEvent{Phase: agent.PhaseImplement, Previous: agent.PhaseExplore, Reason: agent.PhaseReasonWriteTool}, eventType: "run_phase_changed", assert: assertString("phase", "implement")},
		{name: "provider retry", event: agent.ProviderRetryEvent{Attempt: 2, Delay: time.Second, Provider: "test", ErrorType: "overloaded"}, eventType: "provider_retry", assert: assertString("provider", "test")},
		{name: "progress", event: agent.ProgressEvent{Kind: agent.ProgressToolBlocked, Repetition: 3, ToolUseID: "tool-1", Message: "blocked"}, eventType: "progress", assert: assertString("kind", "tool_blocked")},
		{name: "thinking delta", event: agent.ThinkingEvent{Text: "reason"}, eventType: "thinking_delta", assert: assertString("text", "reason")},
		{name: "text delta", event: agent.TextEvent{Text: "answer"}, eventType: "text_delta", assert: assertString("text", "answer")},
		{name: "tool call started", event: agent.ToolCallStartEvent{ToolUseID: "tool-1", ToolName: "read_file"}, eventType: "tool_call_started", assert: assertString("tool_name", "read_file")},
		{name: "tool call delta", event: agent.ToolCallDeltaEvent{ToolUseID: "tool-1", Text: "partial"}, eventType: "tool_call_delta", assert: assertString("text", "partial")},
		{name: "tool call completed", event: agent.ToolCallCompleteEvent{ToolUseID: "tool-1", ToolName: "read_file", Arguments: map[string]any{"path": "README.md"}}, eventType: "tool_call_completed", assert: assertString("tool_name", "read_file")},
		{name: "tool execution started", event: agent.ToolExecutionStartEvent{ToolUseID: "tool-1", ToolName: "read_file"}, eventType: "tool_execution_started", assert: assertString("tool_name", "read_file")},
		{name: "tool result", event: agent.ToolResultEvent{ToolUseID: "tool-1", ToolName: "read_file", Content: "contents", IsError: false}, eventType: "tool_result", assert: assertString("content", "contents")},
		{name: "subagent started", event: agent.SubagentStartEvent{SubagentID: "child-1", ParentSessionID: "session-1", SessionID: "child-session", Task: "inspect"}, eventType: "subagent_started", assert: assertString("subagent_id", "child-1")},
		{name: "subagent event", event: agent.SubagentEvent{SubagentID: "child-1", Event: agent.TextEvent{Text: "finding"}}, eventType: "subagent_event", assert: assertString("event_type", "text_delta")},
		{name: "subagent finished", event: agent.SubagentStopEvent{SubagentID: "child-1", SessionID: "child-session", Status: "completed"}, eventType: "subagent_finished", assert: assertString("status", "completed")},
		{name: "turn finished", event: agent.TurnEndEvent{Status: agent.TurnCompleted, StopReason: agent.StopEndTurn, ProviderReason: "stop", Usage: llm.UsageInfo{InputTokens: 10, OutputTokens: 3, TotalTokens: 13}}, eventType: "turn_finished", assert: assertString("status", "completed")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			encoder := NewEncoder(&output)
			if err := encoder.EncodeAgentEvent("session-1", "turn-1", test.event); err != nil {
				t.Fatal(err)
			}

			decoded := decodeEvent(t, output.String())
			if decoded.Version != 1 || decoded.Sequence != 1 || decoded.Type != test.eventType {
				t.Fatalf("event = %+v", decoded)
			}
			if decoded.SessionID != "session-1" || decoded.TurnID != "turn-1" {
				t.Fatalf("identity = %+v", decoded)
			}
			if test.assert != nil {
				test.assert(t, decoded.Data)
			}
		})
	}
}

func TestEncoderUsesMonotonicSequenceAndOneLinePerEvent(t *testing.T) {
	var output bytes.Buffer
	encoder := NewEncoder(&output)
	if err := encoder.EncodeTurnStarted("session-1", "turn-1"); err != nil {
		t.Fatal(err)
	}
	if err := encoder.EncodeAgentEvent("session-1", "turn-1", agent.TextEvent{Text: "\x1b[31manswer"}); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), output.String())
	}
	first := decodeEvent(t, lines[0])
	second := decodeEvent(t, lines[1])
	if first.Sequence != 1 || second.Sequence != 2 {
		t.Fatalf("sequences = %d, %d", first.Sequence, second.Sequence)
	}
	if bytes.Contains(output.Bytes(), []byte{0x1b}) {
		t.Fatalf("wire output contains raw ANSI escape: %q", output.String())
	}
}

func TestEncoderRejectsLegacyTerminalEvents(t *testing.T) {
	var output bytes.Buffer
	err := NewEncoder(&output).EncodeAgentEvent("session-1", "turn-1", agent.DoneEvent{})
	if err == nil || !strings.Contains(err.Error(), "unsupported agent event") {
		t.Fatalf("error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("unexpected output: %q", output.String())
	}
}

func TestEncoderIncludesTerminalErrorMessage(t *testing.T) {
	var output bytes.Buffer
	want := errors.New("stream failed")
	event := agent.TurnEndEvent{Status: agent.TurnFailed, StopReason: agent.StopAgentError, Err: want}
	if err := NewEncoder(&output).EncodeAgentEvent("session-1", "turn-1", event); err != nil {
		t.Fatal(err)
	}
	decoded := decodeEvent(t, output.String())
	errorData, ok := decoded.Data["error"].(map[string]any)
	if !ok || errorData["message"] != want.Error() {
		t.Fatalf("error data = %#v", decoded.Data["error"])
	}
}

type decodedEvent struct {
	Version   int            `json:"version"`
	Sequence  uint64         `json:"sequence"`
	Type      string         `json:"type"`
	SessionID string         `json:"session_id"`
	TurnID    string         `json:"turn_id"`
	Data      map[string]any `json:"data"`
}

func decodeEvent(t *testing.T, line string) decodedEvent {
	t.Helper()
	var event decodedEvent
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		t.Fatalf("decode %q: %v", line, err)
	}
	return event
}

func assertString(key, want string) func(*testing.T, map[string]any) {
	return func(t *testing.T, data map[string]any) {
		t.Helper()
		if data[key] != want {
			t.Fatalf("%s = %#v, want %q", key, data[key], want)
		}
	}
}
