package conversation

import (
	"context"
	"strings"
	"testing"
	"time"
)

type serviceStore struct {
	metadata SessionMetadata
	messages []StoredMessage
}

func (s *serviceStore) CreateSession(_ context.Context, metadata SessionMetadata) error {
	s.metadata = metadata
	return nil
}
func (s *serviceStore) GetSession(_ context.Context, id string) (SessionMetadata, error) {
	if id != s.metadata.ID {
		return SessionMetadata{}, ErrStoreSessionNotFound
	}
	return s.metadata, nil
}
func (s *serviceStore) ListSessions(_ context.Context, _ string, _ int) ([]SessionMetadata, error) {
	return []SessionMetadata{s.metadata}, nil
}
func (s *serviceStore) RenameSession(_ context.Context, _ string, title string) error {
	s.metadata.Title = title
	return nil
}
func (s *serviceStore) DeleteSession(_ context.Context, _ string) error { return nil }
func (s *serviceStore) ListMessages(_ context.Context, _ string) ([]StoredMessage, error) {
	return append([]StoredMessage(nil), s.messages...), nil
}

func TestResumeRestoresOnlyCompleteTurnsAndAddsBoundary(t *testing.T) {
	store := &serviceStore{metadata: SessionMetadata{ID: "session-1", Title: "test", Workspace: "/workspace", CreatedAt: time.Now().Add(-time.Hour), UpdatedAt: time.Now()}}
	store.messages = []StoredMessage{
		{ID: "message-1", SessionID: "session-1", TurnID: "turn-1", Role: USER, Content: "first", TurnStatus: TurnComplete},
		{ID: "message-2", SessionID: "session-1", TurnID: "turn-2", Role: USER, Content: "unfinished", TurnStatus: TurnOpen},
	}
	service, err := NewService(store, "/workspace", "system")
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Resume(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.Messages.History) != 2 {
		t.Fatalf("expected complete history plus boundary, got %+v", current.Messages.History)
	}
	if current.Messages.History[0].Content != "first" || !strings.Contains(current.Messages.History[1].Content, "context boundary") {
		t.Fatalf("unexpected resumed history: %+v", current.Messages.History)
	}
}
