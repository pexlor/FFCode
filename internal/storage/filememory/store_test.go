package filememory

import (
	"context"
	"errors"
	"testing"
	"time"

	"MyCode/internal/memory"
)

func TestExtractionCompletionIsIdempotent(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	candidate := memory.ExtractionCandidate{SessionID: "session-1", Workspace: "/tmp/project", SourceVersion: 1, TranscriptHash: "hash-1"}
	claim, err := store.ClaimExtraction(ctx, candidate, "worker-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	raw := memory.RawMemory{ID: "raw-1", SessionID: candidate.SessionID, Workspace: candidate.Workspace, SourceVersion: 1, TranscriptHash: candidate.TranscriptHash, GeneratedAt: time.Now(), PromptVersion: 1}
	if err := store.CompleteExtraction(ctx, claim, raw); err != nil {
		t.Fatal(err)
	}

	second, err := store.ClaimExtraction(ctx, candidate, "worker-2", time.Minute)
	if !errors.Is(err, memory.ErrAlreadyProcessed) || second.Token != "" {
		t.Fatalf("expected already processed, got claim=%+v err=%v", second, err)
	}
	inputs, err := store.ListConsolidationInputs(ctx, 10, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].ID != raw.ID {
		t.Fatalf("unexpected inputs: %+v", inputs)
	}
}

func TestExtractionRejectsWrongOwnershipToken(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claim, err := store.ClaimExtraction(ctx, memory.ExtractionCandidate{SessionID: "session-1", Workspace: "/tmp/project", SourceVersion: 1, TranscriptHash: "hash-1"}, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	claim.Token = "wrong"
	err = store.CompleteExtraction(ctx, claim, memory.RawMemory{ID: "raw-1", SessionID: "session-1", TranscriptHash: "hash-1", SourceVersion: 1})
	if !errors.Is(err, memory.ErrLeaseLost) {
		t.Fatalf("expected lease lost, got %v", err)
	}
}

func TestSnapshotCommitUsesLeaseAndOptimisticVersion(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	claim, err := store.ClaimConsolidation(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := memory.MemorySnapshot{Version: 1, Summary: "Use rg.", CreatedAt: time.Now()}
	if err := store.CommitSnapshot(ctx, claim, 0, snapshot); err != nil {
		t.Fatal(err)
	}
	active, err := store.ActiveSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil || active.Version != 1 || active.Summary != "Use rg." {
		t.Fatalf("unexpected active snapshot: %+v", active)
	}

	claim2, err := store.ClaimConsolidation(ctx, "worker-2", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CommitSnapshot(ctx, claim2, 0, memory.MemorySnapshot{Version: 1, Summary: "stale"}); !errors.Is(err, memory.ErrVersionConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
}
