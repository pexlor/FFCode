package fileconversation

import (
	contextmanager "FFCode/internal/context"
	"FFCode/internal/conversation"
	"context"
	"os"
	"path/filepath"
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
		Role: conversation.ASSISTANT, Content: "hello", TurnStatus: conversation.TurnComplete,
		Thinking: []conversation.StoredThinkingBlock{{Thinking: "reasoning", Signature: "signed"}},
	}
	if err := store.AppendMessage(ctx, message); err != nil {
		t.Fatal(err)
	}

	got, err := store.ListMessages(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Content != "hello" || len(got[0].Thinking) != 1 || got[0].Thinking[0].Signature != "signed" {
		t.Fatalf("messages = %#v, want one persisted message", got)
	}
}

func TestListMessagesRecoversValidPrefixAndQuarantinesCorruptTail(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	metadata := conversation.SessionMetadata{ID: "session-recovery", Workspace: root, CreatedAt: time.Now()}
	if err := store.CreateSession(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendMessage(ctx, conversation.StoredMessage{ID: "message-1", SessionID: metadata.ID, TurnID: "turn-1", Role: conversation.USER, Content: "hello", TurnStatus: conversation.TurnOpen}); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(root, metadata.ID, "transcript.jsonl")
	if err := os.WriteFile(transcript, append(mustRead(t, transcript), []byte("{broken\n")...), 0o600); err != nil {
		t.Fatal(err)
	}
	messages, err := store.ListMessages(ctx, metadata.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].ID != "message-1" {
		t.Fatalf("unexpected recovered messages: %+v", messages)
	}
	quarantine, err := filepath.Glob(filepath.Join(root, metadata.ID, "quarantine", "transcript-*.jsonl"))
	if err != nil || len(quarantine) != 1 {
		t.Fatalf("expected quarantined transcript tail, got %v %v", quarantine, err)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ conversation.Repository = (*Store)(nil)
var _ contextmanager.ConversationStore = (*Store)(nil)
