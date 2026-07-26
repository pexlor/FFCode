package memory

import (
	"context"
	"errors"
	"time"
)

var (
	ErrAlreadyProcessed = errors.New("memory extraction already processed")
	ErrLeaseHeld        = errors.New("memory job lease is held")
	ErrLeaseLost        = errors.New("memory job lease lost")
	ErrVersionConflict  = errors.New("memory snapshot version conflict")
	ErrInvalidMemory    = errors.New("invalid memory")
)

type MemoryKind string

const (
	UserPreference MemoryKind = "user_preference"
	Correction     MemoryKind = "correction"
	ProjectFact    MemoryKind = "project_fact"
	Reference      MemoryKind = "reference"
)

type Evidence struct {
	MessageID string `json:"message_id"`
	TurnID    string `json:"turn_id"`
	Quote     string `json:"quote"`
}

type MemoryItem struct {
	Key        string     `json:"key"`
	Kind       MemoryKind `json:"kind"`
	Content    string     `json:"content"`
	Evidence   []Evidence `json:"evidence"`
	Confidence float64    `json:"confidence"`
	Scope      string     `json:"scope,omitempty"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
}

type RawMemory struct {
	ID             string       `json:"id"`
	SessionID      string       `json:"session_id"`
	Workspace      string       `json:"workspace"`
	SourceVersion  int          `json:"source_version"`
	TranscriptHash string       `json:"transcript_hash"`
	Categories     []MemoryItem `json:"categories,omitempty"`
	SessionSummary string       `json:"session_summary,omitempty"`
	GeneratedAt    time.Time    `json:"generated_at"`
	ExtractorModel string       `json:"extractor_model,omitempty"`
	PromptVersion  int          `json:"prompt_version"`
}

type ConsolidatedEntry struct {
	Key             string     `json:"key"`
	Kind            MemoryKind `json:"kind"`
	Content         string     `json:"content"`
	SourceMemoryIDs []string   `json:"source_memory_ids"`
	Confidence      float64    `json:"confidence"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	UsageCount      int        `json:"usage_count,omitempty"`
	LastUsedAt      *time.Time `json:"last_used_at,omitempty"`
	Status          string     `json:"status"`
}

type MemorySnapshot struct {
	Version           int                 `json:"version"`
	PreviousVersion   int                 `json:"previous_version"`
	InputWatermark    string              `json:"input_watermark,omitempty"`
	InputRawMemoryIDs []string            `json:"input_raw_memory_ids,omitempty"`
	Entries           []ConsolidatedEntry `json:"entries,omitempty"`
	Summary           string              `json:"summary"`
	Detailed          string              `json:"detailed,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	Model             string              `json:"model,omitempty"`
	PromptVersion     int                 `json:"prompt_version"`
}

type ExtractionCandidate struct {
	SessionID      string    `json:"session_id"`
	Workspace      string    `json:"workspace"`
	SourceVersion  int       `json:"source_version"`
	TranscriptHash string    `json:"transcript_hash"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}

type ExtractionClaim struct {
	Candidate ExtractionCandidate `json:"candidate"`
	OwnerID   string              `json:"owner_id"`
	Token     string              `json:"token"`
	ExpiresAt time.Time           `json:"expires_at"`
}

type ConsolidationClaim struct {
	OwnerID   string    `json:"owner_id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Store interface {
	ClaimExtraction(context.Context, ExtractionCandidate, string, time.Duration) (ExtractionClaim, error)
	CompleteExtraction(context.Context, ExtractionClaim, RawMemory) error
	FailExtraction(context.Context, ExtractionClaim, string, time.Time) error
	ListConsolidationInputs(context.Context, int, time.Time) ([]RawMemory, error)
	ClaimConsolidation(context.Context, string, time.Duration) (ConsolidationClaim, error)
	RenewConsolidation(context.Context, ConsolidationClaim, time.Duration) error
	ReleaseConsolidation(context.Context, ConsolidationClaim) error
	CommitSnapshot(context.Context, ConsolidationClaim, int, MemorySnapshot) error
	ActiveSnapshot(context.Context) (*MemorySnapshot, error)
	Summary(context.Context) (string, error)
}
