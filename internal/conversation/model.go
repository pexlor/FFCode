package conversation

import "time"

// SessionMetadata is the lightweight session representation used by lifecycle
// commands and persistence implementations.
type SessionMetadata struct {
	ID            string    `json:"session_id"`
	Title         string    `json:"title"`
	Workspace     string    `json:"workspace"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	MessageCount  int       `json:"message_count"`
	FormatVersion int       `json:"format_version"`
}

// Session is the aggregate root for all model context belonging to one
// conversation. History retains text, thinking, tool calls, and tool results.
type Session struct {
	ID             string
	Title          string
	Workspace      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
	Persisted      bool
	ExplicitTitle  bool
	SystemPrompt   string
	LongTermMemory string
	History        []Message

	memoryProvider MemoryProvider
	useMemory      bool
}

type TurnStatus string

const (
	TurnOpen     TurnStatus = "open"
	TurnComplete TurnStatus = "complete"
)

type ResultState string

const (
	ResultFull      ResultState = "full"
	ResultReference ResultState = "reference"
	ResultDropped   ResultState = "dropped"
)

type StoredToolUse struct {
	ToolUseID string         `json:"tool_use_id"`
	ToolName  string         `json:"tool_name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type StoredToolResult struct {
	ToolUseID  string      `json:"tool_use_id"`
	ToolName   string      `json:"tool_name"`
	Content    string      `json:"content"`
	IsError    bool        `json:"is_error"`
	ArtifactID string      `json:"artifact_id,omitempty"`
	State      ResultState `json:"state,omitempty"`
}

type StoredThinkingBlock struct {
	Thinking  string `json:"thinking"`
	Signature string `json:"signature,omitempty"`
}

// StoredMessage is the durable transcript representation. Context compaction
// may create views of it, but never owns or mutates the underlying fact model.
type StoredMessage struct {
	ID          string                `json:"id"`
	SessionID   string                `json:"session_id"`
	TurnID      string                `json:"turn_id"`
	Iteration   int                   `json:"iteration,omitempty"`
	Role        string                `json:"role"`
	Content     string                `json:"content,omitempty"`
	Thinking    []StoredThinkingBlock `json:"thinking,omitempty"`
	ToolUses    []StoredToolUse       `json:"tool_uses,omitempty"`
	ToolResults []StoredToolResult    `json:"tool_results,omitempty"`
	CreatedAt   time.Time             `json:"created_at"`
	TurnStatus  TurnStatus            `json:"turn_status,omitempty"`
}
