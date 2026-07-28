package protocol

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"MyCode/internal/agent"
	"MyCode/internal/llm"
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
	eventType, data, err := agentEventData(event)
	if err != nil {
		return err
	}
	return e.encode(eventType, sessionID, turnID, data)
}

func agentEventData(event agent.AgentEvent) (string, any, error) {
	var eventType string
	var data any
	switch item := event.(type) {
	case agent.TextEvent:
		eventType, data = "text_delta", textData{Text: item.Text}
	case agent.ThinkingStartEvent:
		eventType, data = "thinking_started", struct{}{}
	case agent.RunPhaseEvent:
		eventType, data = "run_phase_changed", runPhaseData{Phase: string(item.Phase), Previous: string(item.Previous), Reason: string(item.Reason)}
	case agent.ProviderRetryEvent:
		eventType, data = "provider_retry", providerRetryData{Attempt: item.Attempt, DelayMS: item.Delay.Milliseconds(), Provider: item.Provider, ErrorType: item.ErrorType}
	case agent.ProgressEvent:
		eventType, data = "progress", progressData{Kind: string(item.Kind), Repetition: item.Repetition, ToolUseID: item.ToolUseID, Message: item.Message}
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
	case agent.SubagentStartEvent:
		eventType, data = "subagent_started", subagentStartData{
			SubagentID: item.SubagentID, ParentSessionID: item.ParentSessionID, SessionID: item.SessionID, Task: item.Task,
		}
	case agent.SubagentEvent:
		childType, childData, err := agentEventData(item.Event)
		if err != nil {
			return "", nil, err
		}
		eventType, data = "subagent_event", subagentEventData{SubagentID: item.SubagentID, EventType: childType, Data: childData}
	case agent.SubagentStopEvent:
		finished := subagentFinishedData{SubagentID: item.SubagentID, SessionID: item.SessionID, Status: item.Status, Usage: encodeUsage(item.Usage)}
		if item.Err != nil {
			finished.Error = &errorData{Message: item.Err.Error()}
		}
		eventType, data = "subagent_finished", finished
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
		return "", nil, fmt.Errorf("unsupported agent event %T", event)
	}
	return eventType, data, nil
}

func encodeUsage(usage llm.UsageInfo) usageData {
	return usageData{
		InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, TotalTokens: usage.TotalTokens,
		CacheReadTokens: usage.CacheReadTokens, CacheCreationTokens: usage.CacheCreationTokens,
	}
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
