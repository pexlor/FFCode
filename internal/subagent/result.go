package subagent

import (
	"MyCode/internal/agent"
	"MyCode/internal/llm"
)

type Status string

const (
	StatusCompleted      Status = "completed"
	StatusFailed         Status = "failed"
	StatusCanceled       Status = "canceled"
	StatusBudgetExceeded Status = "budget_exhausted"
	StatusRejected       Status = "rejected"
)

type Request struct {
	ParentSessionID   string
	Workspace         string
	Task              string
	AdditionalContext string
	Budget            agent.RunBudget
}

type Evidence struct {
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	Content   string `json:"content"`
	Important bool   `json:"important,omitempty"`
}

type Usage struct {
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
}

func usageFromLLM(value llm.UsageInfo) Usage {
	return Usage{
		InputTokens: value.InputTokens, OutputTokens: value.OutputTokens, TotalTokens: value.TotalTokens,
		CacheReadTokens: value.CacheReadTokens, CacheCreationTokens: value.CacheCreationTokens,
	}
}

func (u Usage) llmUsage() llm.UsageInfo {
	return llm.UsageInfo{
		InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, TotalTokens: u.TotalTokens,
		CacheReadTokens: u.CacheReadTokens, CacheCreationTokens: u.CacheCreationTokens,
	}
}

type Result struct {
	SubagentID string           `json:"subagent_id,omitempty"`
	SessionID  string           `json:"session_id,omitempty"`
	Status     Status           `json:"status"`
	Summary    string           `json:"summary,omitempty"`
	Evidence   []Evidence       `json:"evidence,omitempty"`
	FilesRead  []string         `json:"files_read,omitempty"`
	Usage      Usage            `json:"usage"`
	StopReason agent.StopReason `json:"stop_reason,omitempty"`
	Error      string           `json:"error,omitempty"`

	Err error `json:"-"`
}
