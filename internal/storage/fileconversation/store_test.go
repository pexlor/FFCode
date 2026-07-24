package fileconversation

import (
	contextmanager "MyCode/internal/context"
	"MyCode/internal/conversation"
	"context"
	"testing"
	"time"
)

func TestStorePersistsSessionAndTranscript(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	metadata := conversation.SessionMetadata{
		ID: "session-test", Title: "test", Workspace: "/workspace",
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := store.CreateSession(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	message := conversation.StoredMessage{
		ID: "message-000001", SessionID: metadata.ID, TurnID: "turn-000001",
		Role: conversation.USER, Content: "hello", TurnStatus: conversation.TurnOpen,
	}
	if err := store.AppendMessage(ctx, message); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListMessages(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "hello" {
		t.Fatalf("messages = %#v, want one persisted message", got)
	}
}

var _ conversation.Repository = (*Store)(nil)
var _ contextmanager.ConversationStore = (*Store)(nil)
