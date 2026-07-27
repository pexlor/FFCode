package contextmanager

import (
	"MyCode/internal/conversation"
	"time"
)

// Compatibility aliases keep the context algorithms focused while making the
// conversation package the authoritative owner of transcript types.
type SessionMetadata = conversation.SessionMetadata
type TurnStatus = conversation.TurnStatus
type ResultState = conversation.ResultState
type StoredToolUse = conversation.StoredToolUse
type StoredToolResult = conversation.StoredToolResult
type StoredThinkingBlock = conversation.StoredThinkingBlock
type StoredMessage = conversation.StoredMessage

const (
	TurnOpen        = conversation.TurnOpen
	TurnComplete    = conversation.TurnComplete
	ResultFull      = conversation.ResultFull
	ResultReference = conversation.ResultReference
	ResultDropped   = conversation.ResultDropped
)

// ToolArtifact 描述一个已经从模型上下文卸载到磁盘的完整工具结果。
// ContentSHA256 用于读取时校验，避免模型依据损坏或被替换的归档继续推理。
type ToolArtifact struct {
	ID               string    `json:"id"`
	SessionID        string    `json:"session_id"`
	ToolUseID        string    `json:"tool_use_id"`
	ToolName         string    `json:"tool_name"`
	ArgumentsSummary string    `json:"arguments_summary,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	IsError          bool      `json:"is_error"`
	ByteSize         int64     `json:"byte_size"`
	TokenEstimate    int       `json:"token_estimate"`
	ContentSHA256    string    `json:"content_sha256"`
	StoragePath      string    `json:"storage_path"`
	Preview          string    `json:"preview,omitempty"`
}

// SummarySnapshot 是一次已提交的增量摘要及其压缩检查点。
// CoveredThroughMessageID 之前（含该消息）的原文不会再次进入 ContextView，
// 但仍永久保留在 transcript 中供审计和精确恢复。
type SummarySnapshot struct {
	Version                 int       `json:"version"`
	SessionID               string    `json:"session_id"`
	CoveredThroughMessageID string    `json:"covered_through_message_id,omitempty"`
	CoveredThroughTurnID    string    `json:"covered_through_turn_id,omitempty"`
	PreviousSummaryVersion  int       `json:"previous_summary_version,omitempty"`
	Content                 string    `json:"content"`
	TokenEstimate           int       `json:"token_estimate,omitempty"`
	ArtifactIDs             []string  `json:"artifact_ids,omitempty"`
	CreatedAt               time.Time `json:"created_at"`
}
