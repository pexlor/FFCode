package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"MyCode/internal/agent"
)

type Encoder struct {
	mu       sync.Mutex
	encoder  *json.Encoder
	sequence uint64
}

func NewEncoder(writer io.Writer) *Encoder {
	return &Encoder{encoder: json.NewEncoder(writer)}
}

func (e *Encoder) EncodeTurnStarted(sessionID, turnID string) error {
	return e.encode("turn_started", sessionID, turnID, struct{}{})
}

func (e *Encoder) EncodeAgentEvent(sessionID, turnID string, event agent.AgentEvent) error {
	var eventType string
	var data any
	switch item := event.(type) {
	case agent.TextEvent:
		eventType, data = "text_delta", textData{Text: item.Text}
	case agent.ThinkingStartEvent:
		eventType, data = "thinking_started", struct{}{}
	case agent.RunPhaseEvent:
		eventType, data = "run_phase_changed", runPhaseData{Phase: string(item.Phase), Previous: string(item.Previous), Reason: string(item.Reason)}
	case agent.ThinkingEvent:
		eventType, data = "thinking_delta", textData{Text: item.Text}
	case agent.ToolCallStartEvent:
		eventType, data = "tool_call_started", toolData{ToolUseID: item.ToolUseID, ToolName: item.ToolName}
	case agent.ToolCallDeltaEvent:
		eventType, data = "tool_call_delta", toolData{ToolUseID: item.ToolUseID, Text: item.Text}
	case agent.ToolCallCompleteEvent:
		eventType, data = "tool_call_completed", toolData{ToolUseID: item.ToolUseID, ToolName: item.ToolName, Arguments: item.Arguments}
	case agent.ToolExecutionStartEvent:
		eventType, data = "tool_execution_started", toolData{ToolUseID: item.ToolUseID, ToolName: item.ToolName}
	case agent.ToolResultEvent:
		eventType, data = "tool_result", toolData{ToolUseID: item.ToolUseID, ToolName: item.ToolName, Content: item.Content, IsError: item.IsError}
	case agent.TurnEndEvent:
		terminal := turnFinishedData{
			Status:         string(item.Status),
			StopReason:     string(item.StopReason),
			ProviderReason: item.ProviderReason,
			Usage: usageData{
				InputTokens:         item.Usage.InputTokens,
				OutputTokens:        item.Usage.OutputTokens,
				TotalTokens:         item.Usage.TotalTokens,
				CacheReadTokens:     item.Usage.CacheReadTokens,
				CacheCreationTokens: item.Usage.CacheCreationTokens,
			},
		}
		if item.Err != nil {
			terminal.Error = &errorData{Message: item.Err.Error()}
		}
		eventType, data = "turn_finished", terminal
	default:
		return fmt.Errorf("unsupported agent event %T", event)
	}
	return e.encode(eventType, sessionID, turnID, data)
}

func (e *Encoder) encode(eventType, sessionID, turnID string, data any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next := e.sequence + 1
	if err := e.encoder.Encode(Event{
		Version: Version, Sequence: next, Type: eventType,
		SessionID: sessionID, TurnID: turnID, Data: data,
	}); err != nil {
		return err
	}
	e.sequence = next
	return nil
}
