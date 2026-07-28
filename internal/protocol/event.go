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
