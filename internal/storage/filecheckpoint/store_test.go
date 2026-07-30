package filecheckpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"FFCode/internal/agent"
	"FFCode/internal/conversation"
)

func TestStoreWritesAtomicGenerationsAndLoadsLatest(t *testing.T) {
	root := t.TempDir()
	store, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	first := agent.RunCheckpoint{Version: agent.CheckpointFormatVersion, SessionID: "session-1", Boundary: agent.CheckpointModel}
	second := agent.RunCheckpoint{
		Version: agent.CheckpointFormatVersion, SessionID: "session-1", Boundary: agent.CheckpointTools,
		History: []conversation.Message{{Role: conversation.USER, Content: "latest"}},
	}
	if err := store.Save(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	third := second
	third.History[0].Content = "newest"
	if err := store.Save(context.Background(), third); err != nil {
		t.Fatal(err)
	}

	loaded, err := store.Load(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Generation != 3 || loaded.Boundary != agent.CheckpointTools || loaded.History[0].Content != "newest" {
		t.Fatalf("loaded = %+v", loaded)
	}
	entries, err := os.ReadDir(filepath.Join(root, "session-1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("generation directory entries = %d, want manifest plus two generations", len(entries))
	}
	if _, err := os.Stat(filepath.Join(root, "session-1", "generation-00000000000000000001.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest generation still exists: %v", err)
	}
}

func TestStoreRejectsUnsafeSessionIDAndMissingCheckpoint(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), "../escape"); err == nil {
		t.Fatal("unsafe session ID was accepted")
	}
	if _, err := store.Load(context.Background(), "missing"); !errors.Is(err, agent.ErrCheckpointNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}
