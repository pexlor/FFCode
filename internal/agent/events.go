package agent

import (
	"time"

	"MyCode/internal/llm"
)

type AgentEvent interface{ agentEvent() }

type TurnStatus string

const (
	TurnCompleted  TurnStatus = "completed"
	TurnIncomplete TurnStatus = "incomplete"
	TurnFailed     TurnStatus = "failed"
	TurnCancelled  TurnStatus = "cancelled"
)

type StopReason string

const (
	StopEndTurn          StopReason = "end_turn"
	StopMaxTokens        StopReason = "max_tokens"
	StopCancelled        StopReason = "cancelled"
	StopDeadlineExceeded StopReason = "deadline_exceeded"
	StopProviderError    StopReason = "provider_error"
	StopBudgetExceeded   StopReason = "budget_exhausted"
	StopNoProgress       StopReason = "no_progress"
	StopAgentError       StopReason = "agent_error"
)

type TextEvent struct {
	Text string
}

type ThinkingEvent struct {
	Text string
}

// ThinkingStartEvent indicates that the agent has started a model request.
// It is emitted even when the provider does not expose reasoning tokens, so a
// UI can still show that the conversation is making progress.
type ThinkingStartEvent struct{}

type RunPhaseEvent struct {
	Phase    RunPhase
	Previous RunPhase
	Reason   PhaseReason
}

type ProviderRetryEvent struct {
	Attempt   int
	Delay     time.Duration
	Provider  string
	ErrorType string
}

type ProgressEvent struct {
	Kind       ProgressKind
	Repetition int
	ToolUseID  string
	Message    string
}

type QualityWarningEvent struct {
	Code     string
	Severity WarningSeverity
	Message  string
	Evidence []string
}

type ToolCallStartEvent struct {
	ToolUseID string
	ToolName  string
}

type ToolCallDeltaEvent struct {
	ToolUseID string
	Text      string
}

type ToolCallCompleteEvent struct {
	ToolUseID string
	ToolName  string
	Arguments map[string]any
}

// ToolExecutionStartEvent is emitted immediately before a requested tool is
// executed. ToolCallStartEvent only means that the model started describing a
// call; this event represents the actual side effecting operation.
type ToolExecutionStartEvent struct {
	ToolUseID string
	ToolName  string
}

type ToolResultEvent struct {
	ToolUseID string
	ToolName  string
	Content   string
	IsError   bool
}

type SubagentStartEvent struct {
	SubagentID      string
	ParentSessionID string
	SessionID       string
	Task            string
}

type SubagentEvent struct {
	SubagentID string
	Event      AgentEvent
}

type SubagentStopEvent struct {
	SubagentID string
	SessionID  string
	Status     string
	Usage      llm.UsageInfo
	Err        error
}

type TurnEndEvent struct {
	Status         TurnStatus
	StopReason     StopReason
	ProviderReason string
	Usage          llm.UsageInfo
	Err            error
}

// DoneEvent and ErrorEvent remain for source compatibility. Agent.Run emits
// TurnEndEvent so callers do not need to infer lifecycle state from event type.
type DoneEvent struct {
	StopReason string
	Usage      llm.UsageInfo
}

type ErrorEvent struct {
	Err error
}

func (TextEvent) agentEvent()               {}
func (ThinkingEvent) agentEvent()           {}
func (ThinkingStartEvent) agentEvent()      {}
func (RunPhaseEvent) agentEvent()           {}
func (ProviderRetryEvent) agentEvent()      {}
func (ProgressEvent) agentEvent()           {}
func (QualityWarningEvent) agentEvent()     {}
func (ToolCallStartEvent) agentEvent()      {}
func (ToolCallDeltaEvent) agentEvent()      {}
func (ToolCallCompleteEvent) agentEvent()   {}
func (ToolExecutionStartEvent) agentEvent() {}
func (ToolResultEvent) agentEvent()         {}
func (SubagentStartEvent) agentEvent()      {}
func (SubagentEvent) agentEvent()           {}
func (SubagentStopEvent) agentEvent()       {}
func (TurnEndEvent) agentEvent()            {}
func (DoneEvent) agentEvent()               {}
func (ErrorEvent) agentEvent()              {}
