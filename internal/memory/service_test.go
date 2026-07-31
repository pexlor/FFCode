package memory_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"FFCode/internal/conversation"
	"FFCode/internal/memory"
	filememory "FFCode/internal/storage/filememory"
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
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.ActiveSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || snapshot.Version != 1 {
		t.Fatalf("unchanged inputs created another snapshot: %+v", snapshot)
	}
}

type failingExtractor struct{}

func (failingExtractor) Extract(context.Context, memory.ExtractRequest) (memory.RawMemory, error) {
	return memory.RawMemory{}, errors.New("extract failed")
}

func TestServiceStartRunsImmediatelyAndReportsExtractionErrors(t *testing.T) {
	root := t.TempDir()
	store, err := filememory.New(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	source := fakeTranscriptSource{sessions: []conversation.SessionMetadata{{ID: "session-idle", Workspace: "/project", UpdatedAt: now}}, messages: map[string][]conversation.StoredMessage{"session-idle": {{ID: "m1", SessionID: "session-idle", TurnID: "t1", Role: conversation.USER, Content: "remember this", TurnStatus: conversation.TurnComplete}}}}
	reported := make(chan error, 1)
	service := memory.Service{Store: store, Source: source, Extractor: failingExtractor{}, OwnerID: "test", Workspace: "/project", MinIdle: time.Minute, ScanInterval: time.Hour, OnError: func(err error) { reported <- err }}
	cancel := service.Start(context.Background())
	defer cancel()
	select {
	case err := <-reported:
		if !strings.Contains(err.Error(), "extract failed") {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("memory worker did not run immediately")
	}
}

type recordingExtractor struct {
	mu       sync.Mutex
	requests []memory.ExtractRequest
}

func (r *recordingExtractor) Extract(_ context.Context, request memory.ExtractRequest) (memory.RawMemory, error) {
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.mu.Unlock()
	message := request.Messages[0]
	return memory.RawMemory{
		ID: fmt.Sprintf("raw-%d", request.SourceVersion), SessionID: request.SessionID, Workspace: request.Workspace,
		SourceVersion: request.SourceVersion, TranscriptHash: request.TranscriptHash, PromptVersion: 1, GeneratedAt: time.Now(),
		Categories: []memory.MemoryItem{{Key: "reference/project/note", Kind: memory.Reference, Content: message.Content, Confidence: 0.9, Evidence: []memory.Evidence{{MessageID: message.ID, TurnID: message.TurnID, Quote: message.Content}}}},
	}, nil
}

func TestServiceExtractsOnlyMessagesAfterSuccessfulWatermark(t *testing.T) {
	store, err := filememory.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Add(-time.Hour)
	source := fakeTranscriptSource{
		sessions: []conversation.SessionMetadata{{ID: "session-idle", Workspace: "/project", UpdatedAt: now}},
		messages: map[string][]conversation.StoredMessage{"session-idle": {{ID: "m1", SessionID: "session-idle", TurnID: "t1", Role: conversation.USER, Content: "first", TurnStatus: conversation.TurnComplete}}},
	}
	extractor := &recordingExtractor{}
	service := memory.Service{Store: store, Source: source, Extractor: extractor, OwnerID: "test", Workspace: "/project", MinIdle: time.Minute}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	source.messages["session-idle"] = append(source.messages["session-idle"], conversation.StoredMessage{ID: "m2", SessionID: "session-idle", TurnID: "t2", Role: conversation.USER, Content: "second", TurnStatus: conversation.TurnComplete})
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	extractor.mu.Lock()
	defer extractor.mu.Unlock()
	if len(extractor.requests) != 2 || len(extractor.requests[1].Messages) != 1 || extractor.requests[1].Messages[0].ID != "m2" {
		t.Fatalf("unexpected incremental requests: %+v", extractor.requests)
	}
}
