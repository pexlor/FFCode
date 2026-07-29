package contextmanager

import (
	"MyCode/internal/conversation"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActivePathsIgnoresWorkingDirectoryVariants(t *testing.T) {
	messages := []StoredMessage{
		{ToolUses: []StoredToolUse{{
			ToolUseID: "call-1",
			Arguments: map[string]any{
				"file_path":         "src/main.go",
				"working_directory": "/repository/root",
				"working-directory": "/repository/root",
				"workingDirectory":  "/repository/root",
			},
		}}},
		{ToolResults: []StoredToolResult{{ToolUseID: "call-1"}}},
	}

	got := activePaths(messages)
	if len(got) != 1 || got[0] != "src/main.go" {
		t.Fatalf("activePaths() = %#v, want only the actual file path", got)
	}
}

func TestDemandLoaderLoadsProjectKnowledge(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("build with go test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rules, err := (DemandLoader{Workspace: root}).LoadRules(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 || rules[0].Content != "build with go test\n" {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

type fakeConversationStore struct{}

func (fakeConversationStore) AppendMessage(context.Context, StoredMessage) error { return nil }
func (fakeConversationStore) ListMessages(context.Context, string) ([]StoredMessage, error) {
	return nil, nil
}
func (fakeConversationStore) ListMessagesAfter(context.Context, string, string) ([]StoredMessage, error) {
	return nil, nil
}
func (fakeConversationStore) SaveToolArtifact(context.Context, ToolArtifact, io.Reader) error {
	return nil
}
func (fakeConversationStore) LoadToolArtifact(context.Context, string, string) (ToolArtifact, io.ReadCloser, error) {
	return ToolArtifact{}, nil, nil
}
func (fakeConversationStore) ActiveSummary(context.Context, string) (*SummarySnapshot, error) {
	return nil, nil
}
func (fakeConversationStore) CommitSummary(context.Context, SummarySnapshot, int) error { return nil }

type countingConversationStore struct {
	fakeConversationStore
	activeSummaryCalls int
	listMessageCalls   int
}

func (s *countingConversationStore) ListMessages(ctx context.Context, sessionID string) ([]StoredMessage, error) {
	s.listMessageCalls++
	return s.fakeConversationStore.ListMessages(ctx, sessionID)
}

func (s *countingConversationStore) ActiveSummary(ctx context.Context, sessionID string) (*SummarySnapshot, error) {
	s.activeSummaryCalls++
	return s.fakeConversationStore.ActiveSummary(ctx, sessionID)
}

func TestContextManagerIncrementallyUpdatesCachedView(t *testing.T) {
	store := &countingConversationStore{}
	manager, err := NewContextManager(ContextManagerConfig{
		Store: store, Estimator: ConservativeEstimator{},
		Model:  ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy: DefaultPolicy(), Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationContext := &ConversationContext{
		SessionID: "session-cache", SystemPrompt: "system",
		History: []conversation.Message{{Role: conversation.USER, Content: "first"}},
	}
	first, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "first"})
	if err != nil {
		t.Fatal(err)
	}
	conversationContext.History = append(conversationContext.History, conversation.Message{Role: conversation.ASSISTANT, Content: "second"})
	second, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if store.activeSummaryCalls != 1 || store.listMessageCalls != 2 {
		t.Fatalf("durable context was rebuilt: summaries=%d messages=%d", store.activeSummaryCalls, store.listMessageCalls)
	}
	if len(second.Messages) != len(first.Messages)+1 || second.Messages[len(second.Messages)-1].Content != "second" {
		t.Fatalf("incremental view = %#v", second.Messages)
	}
}

func TestContextManagerInjectsMemorySummaryWhenEnabled(t *testing.T) {
	manager, err := NewContextManager(ContextManagerConfig{
		Store: fakeConversationStore{}, Estimator: ConservativeEstimator{},
		Model:     ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy:    DefaultPolicy(),
		Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &conversation.Session{ID: "session-1", SystemPrompt: "system", LongTermMemory: "remember rg"}
	view, err := manager.Build(context.Background(), BuildInput{Session: session, CurrentRequest: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.SystemPrompt, "remember rg") {
		t.Fatalf("memory summary missing: %q", view.SystemPrompt)
	}
}

func TestContextManagerUsesRuntimeSystemPrompt(t *testing.T) {
	manager, err := NewContextManager(ContextManagerConfig{
		Store: fakeConversationStore{}, Estimator: ConservativeEstimator{},
		Model:     ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy:    DefaultPolicy(),
		Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	session := &conversation.Session{ID: "session-1", SystemPrompt: "base", LongTermMemory: "remember rg"}
	view, err := manager.Build(context.Background(), BuildInput{
		Session: session, SystemPrompt: "base\n\n# Run phase\nfinalize", CurrentRequest: "hi",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.SystemPrompt, "# Run phase\nfinalize") || !strings.Contains(view.SystemPrompt, "remember rg") {
		t.Fatalf("runtime prompt or memory missing: %q", view.SystemPrompt)
	}
}

func TestMessageConversionPreservesThinkingBlocks(t *testing.T) {
	message := conversation.Message{
		Role:           conversation.ASSISTANT,
		ThinkingBlocks: []conversation.ThinkingBlock{{Thinking: "reasoning", Signature: "signature"}},
	}
	stored := fromMessage(message, "session-1", 1, 1)
	view := (&ContextManager{estimator: ConservativeEstimator{}, model: ModelContextSpec{ModelName: "test"}}).renderView("session-1", "system", nil, []StoredMessage{stored}, nil, nil)
	got := view.Messages[0].ThinkingBlocks
	if len(got) != 1 || got[0] != message.ThinkingBlocks[0] {
		t.Fatalf("thinking blocks = %#v", got)
	}
}
