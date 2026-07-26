package memory_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"MyCode/internal/conversation"
	"MyCode/internal/memory"
	filememory "MyCode/internal/storage/filememory"
)

type fakeTranscriptSource struct {
	sessions []conversation.SessionMetadata
	messages map[string][]conversation.StoredMessage
}

func (f fakeTranscriptSource) ListSessions(_ context.Context, _ string, _ int) ([]conversation.SessionMetadata, error) {
	return f.sessions, nil
}
func (f fakeTranscriptSource) ListMessages(_ context.Context, id string) ([]conversation.StoredMessage, error) {
	return f.messages[id], nil
}

type fakeExtractor struct{}

func (fakeExtractor) Extract(_ context.Context, request memory.ExtractRequest) (memory.RawMemory, error) {
	return memory.RawMemory{ID: "raw-1", SessionID: request.SessionID, Workspace: request.Workspace, SourceVersion: request.SourceVersion, TranscriptHash: request.TranscriptHash, PromptVersion: 1, GeneratedAt: time.Now(), Categories: []memory.MemoryItem{{Key: "reference/project/test", Kind: memory.Reference, Content: "run tests", Confidence: 0.9, Evidence: []memory.Evidence{{MessageID: request.Messages[0].ID, TurnID: request.Messages[0].TurnID, Quote: "run tests"}}}}}, nil
}

func TestServiceRunOnceExtractsOnlyIdleCompleteSessions(t *testing.T) {
	root := t.TempDir()
	store, err := filememory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	source := fakeTranscriptSource{
		sessions: []conversation.SessionMetadata{{ID: "session-idle", Workspace: "/project", UpdatedAt: now.Add(-time.Hour)}, {ID: "session-fresh", Workspace: "/project", UpdatedAt: now.Add(-time.Minute)}},
		messages: map[string][]conversation.StoredMessage{
			"session-idle":  {{ID: "m1", SessionID: "session-idle", TurnID: "t1", Role: conversation.USER, Content: "run tests", TurnStatus: conversation.TurnComplete}},
			"session-fresh": {{ID: "m2", SessionID: "session-fresh", TurnID: "t2", Role: conversation.USER, Content: "fresh", TurnStatus: conversation.TurnComplete}},
		},
	}
	service := memory.Service{Store: store, Source: source, Extractor: fakeExtractor{}, OwnerID: "test", MinIdle: 30 * time.Minute}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	inputs, err := store.ListConsolidationInputs(context.Background(), 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) != 1 || inputs[0].SessionID != "session-idle" {
		t.Fatalf("unexpected inputs: %+v", inputs)
	}
}

func TestServiceRunOnceConsolidatesNewRawMemory(t *testing.T) {
	root := t.TempDir()
	store, err := filememory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	source := fakeTranscriptSource{sessions: []conversation.SessionMetadata{{ID: "session-idle", Workspace: "/project", UpdatedAt: now}}, messages: map[string][]conversation.StoredMessage{"session-idle": {{ID: "m1", SessionID: "session-idle", TurnID: "t1", Role: conversation.USER, Content: "run tests", TurnStatus: conversation.TurnComplete}}}}
	service := memory.Service{Store: store, Source: source, Extractor: fakeExtractor{}, Consolidator: memory.DeterministicConsolidator{}, OwnerID: "test", MinIdle: time.Minute}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	summary, err := store.Summary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary == "" || !strings.Contains(summary, "run tests") {
		t.Fatalf("unexpected summary: %q", summary)
	}
}
