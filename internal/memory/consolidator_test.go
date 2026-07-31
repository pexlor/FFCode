package memory

import (
	"context"
	"strings"
	"testing"
	"time"

	"FFCode/internal/llm"
)

func TestDeterministicConsolidatorDeduplicatesAndPreservesEvidence(t *testing.T) {
	previous := &MemorySnapshot{Version: 2, Entries: []ConsolidatedEntry{{Key: "reference/project/test", Kind: Reference, Content: "go test ./...", Confidence: 0.8, Status: "active"}}}
	inputs := []RawMemory{{ID: "raw-1", SessionID: "session-1", GeneratedAt: time.Now(), Categories: []MemoryItem{{Key: "reference/project/test", Kind: Reference, Content: "go test ./...", Confidence: 0.9, Evidence: []Evidence{{MessageID: "m1", TurnID: "t1", Quote: "go test ./..."}}}}}}
	snapshot, err := (DeterministicConsolidator{}).Consolidate(context.Background(), ConsolidateRequest{Previous: previous, Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Content != "go test ./..." || len(snapshot.Entries[0].SourceMemoryIDs) != 1 {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if snapshot.Summary == "" || snapshot.Detailed == "" {
		t.Fatalf("expected rendered summaries: %+v", snapshot)
	}
}

func TestDeterministicConsolidatorKeepsHigherConfidenceConflict(t *testing.T) {
	inputs := []RawMemory{
		{ID: "raw-old", GeneratedAt: time.Now().Add(-time.Hour), Categories: []MemoryItem{{Key: "user_preference/project/style", Kind: UserPreference, Content: "tabs", Confidence: 0.6, Evidence: []Evidence{{MessageID: "m1", TurnID: "t1", Quote: "tabs"}}}}},
		{ID: "raw-new", GeneratedAt: time.Now(), Categories: []MemoryItem{{Key: "user_preference/project/style", Kind: UserPreference, Content: "spaces", Confidence: 0.9, Evidence: []Evidence{{MessageID: "m2", TurnID: "t2", Quote: "spaces"}}}}},
	}
	snapshot, err := (DeterministicConsolidator{}).Consolidate(context.Background(), ConsolidateRequest{Inputs: inputs})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || snapshot.Entries[0].Content != "spaces" {
		t.Fatalf("unexpected conflict result: %+v", snapshot.Entries)
	}
}

func TestLLMConsolidatorFallsBackWhenModelDropsSourceEntries(t *testing.T) {
	input := RawMemory{ID: "raw-1", GeneratedAt: time.Now(), Categories: []MemoryItem{{Key: "reference/project/test", Kind: Reference, Scope: "workspace", Content: "go test ./...", Confidence: 0.9, Evidence: []Evidence{{MessageID: "m1", TurnID: "t1", Quote: "go test"}}}}}
	consolidator := LLMConsolidator{
		Client:   fakeLLM{events: []llm.StreamEvent{llm.TextStream{Text: `{"entries":[],"summary":"invented"}`}}},
		Model:    "test",
		Fallback: DeterministicConsolidator{},
	}
	snapshot, err := consolidator.Consolidate(context.Background(), ConsolidateRequest{Inputs: []RawMemory{input}})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Entries) != 1 || !strings.Contains(snapshot.Summary, "go test") {
		t.Fatalf("unsafe model output was not replaced: %+v", snapshot)
	}
}

func TestSummaryLimitAndSessionScopeAreEnforced(t *testing.T) {
	entries := []ConsolidatedEntry{
		{Key: "session/note", Kind: Reference, Scope: "session", Content: "do not inject", Confidence: 0.9, Status: "active"},
		{Key: "workspace/note", Kind: Reference, Scope: "workspace", Content: strings.Repeat("x", 100), Confidence: 0.9, Status: "active"},
	}
	summary := renderSummary(entries, 32)
	if strings.Contains(summary, "do not inject") || len([]rune(summary)) > 32 {
		t.Fatalf("summary policy was not enforced: %q", summary)
	}
}
