package protocol

const Version = 1

type Event struct {
	Version   int    `json:"version"`
	Sequence  uint64 `json:"sequence"`
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	TurnID    string `json:"turn_id"`
	Data      any    `json:"data"`
}

type textData struct {
	Text string `json:"text"`
}

type runPhaseData struct {
	Phase    string `json:"phase"`
	Previous string `json:"previous,omitempty"`
	Reason   string `json:"reason"`
}

type providerRetryData struct {
	Attempt   int    `json:"attempt"`
	DelayMS   int64  `json:"delay_ms"`
	Provider  string `json:"provider,omitempty"`
	ErrorType string `json:"error_type,omitempty"`
}

type progressData struct {
	Kind       string `json:"kind"`
	Repetition int    `json:"repetition"`
	ToolUseID  string `json:"tool_use_id,omitempty"`
	Message    string `json:"message"`
}

type qualityWarningData struct {
	Code     string   `json:"code"`
	Severity string   `json:"severity"`
	Message  string   `json:"message"`
	Evidence []string `json:"evidence,omitempty"`
}

type toolData struct {
	ToolUseID string         `json:"tool_use_id"`
	ToolName  string         `json:"tool_name,omitempty"`
	Text      string         `json:"text,omitempty"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type usageData struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

type errorData struct {
	Message string `json:"message"`
}

type turnFinishedData struct {
	Status         string     `json:"status"`
	StopReason     string     `json:"stop_reason"`
	ProviderReason string     `json:"provider_reason,omitempty"`
	Usage          usageData  `json:"usage"`
	Error          *errorData `json:"error,omitempty"`
}

type subagentStartData struct {
	SubagentID      string `json:"subagent_id"`
	ParentSessionID string `json:"parent_session_id"`
	SessionID       string `json:"session_id"`
	Task            string `json:"task"`
}

type subagentEventData struct {
	SubagentID string `json:"subagent_id"`
	EventType  string `json:"event_type"`
	Data       any    `json:"data"`
}

type subagentFinishedData struct {
	SubagentID string     `json:"subagent_id"`
	SessionID  string     `json:"session_id"`
	Status     string     `json:"status"`
	Usage      usageData  `json:"usage"`
	Error      *errorData `json:"error,omitempty"`
}
