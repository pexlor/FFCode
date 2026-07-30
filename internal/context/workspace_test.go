package contextmanager

import (
	"FFCode/internal/conversation"
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
	listAfterCalls     int
	messages           []StoredMessage
	artifacts          []ToolArtifact
	artifactContents   map[string]string
}

func (s *countingConversationStore) AppendMessage(_ context.Context, message StoredMessage) error {
	s.messages = append(s.messages, cloneStoredMessages([]StoredMessage{message})[0])
	return nil
}

func (s *countingConversationStore) ListMessages(ctx context.Context, sessionID string) ([]StoredMessage, error) {
	s.listMessageCalls++
	return cloneStoredMessages(s.messages), nil
}

func (s *countingConversationStore) ListMessagesAfter(_ context.Context, _ string, messageID string) ([]StoredMessage, error) {
	s.listAfterCalls++
	for index, message := range s.messages {
		if message.ID == messageID {
			return cloneStoredMessages(s.messages[index+1:]), nil
		}
	}
	return cloneStoredMessages(s.messages), nil
}

func (s *countingConversationStore) SaveToolArtifact(_ context.Context, artifact ToolArtifact, content io.Reader) error {
	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, artifact)
	if s.artifactContents == nil {
		s.artifactContents = make(map[string]string)
	}
	s.artifactContents[artifact.ID] = string(data)
	return nil
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
	if store.activeSummaryCalls != 1 || store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("durable context was rebuilt: summaries=%d messages=%d after=%d", store.activeSummaryCalls, store.listMessageCalls, store.listAfterCalls)
	}
	if len(second.Messages) != len(first.Messages)+1 || second.Messages[len(second.Messages)-1].Content != "second" {
		t.Fatalf("incremental view = %#v", second.Messages)
	}
}

func TestContextManagerResumeInvalidatesCachedView(t *testing.T) {
	store := &countingConversationStore{}
	manager, err := NewContextManager(ContextManagerConfig{
		Store: store, Estimator: ConservativeEstimator{},
		Model:  ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy: DefaultPolicy(), Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	firstContext := &ConversationContext{
		SessionID: "session-resume", LifecycleKey: "session-resume:new", SystemPrompt: "system",
		History: []conversation.Message{{Role: conversation.USER, Content: "first"}},
	}
	first, err := manager.Build(context.Background(), BuildInput{Context: firstContext, CurrentRequest: "first"})
	if err != nil {
		t.Fatal(err)
	}
	resumedContext := &ConversationContext{
		SessionID: "session-resume", LifecycleKey: "session-resume:resume", SystemPrompt: "system after resume",
		History: append([]conversation.Message(nil), firstContext.History...),
	}
	second, err := manager.Build(context.Background(), BuildInput{Context: resumedContext, CurrentRequest: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if store.activeSummaryCalls != 2 || store.listMessageCalls != 3 {
		t.Fatalf("resume reused cached view: summaries=%d messages=%d", store.activeSummaryCalls, store.listMessageCalls)
	}
	if first == second || second.SystemPrompt != "system after resume" {
		t.Fatalf("resumed view was not rebuilt: first=%p second=%p prompt=%q", first, second, second.SystemPrompt)
	}
}

func TestContextManagerCachedViewRefreshesRulesForActiveSubdirectory(t *testing.T) {
	workspace := t.TempDir()
	ruleDirectory := filepath.Join(workspace, "child", ".agent")
	if err := os.MkdirAll(ruleDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ruleDirectory, "context.md"), []byte("child rule marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := &countingConversationStore{}
	manager, err := NewContextManager(ContextManagerConfig{
		Store: store, Estimator: ConservativeEstimator{},
		Model:  ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy: DefaultPolicy(), Workspace: workspace,
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationContext := &ConversationContext{
		SessionID: "session-rules", LifecycleKey: "session-rules:new", SystemPrompt: "system",
		History: []conversation.Message{{Role: conversation.USER, Content: "inspect child"}},
	}
	first, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "inspect child"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(first.SystemPrompt, "child rule marker") {
		t.Fatalf("child rule loaded before path became active: %q", first.SystemPrompt)
	}
	conversationContext.History = append(conversationContext.History,
		conversation.Message{Role: conversation.ASSISTANT, ToolUses: []conversation.ToolUseBlock{{
			ToolUseID: "call-child", ToolName: "ReadFile", Arguments: map[string]any{"file_path": "child/main.go"},
		}}},
		conversation.Message{Role: conversation.TOOL, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "call-child", Content: "package child"}}},
	)
	second, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "inspect child"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.SystemPrompt, "child rule marker") {
		t.Fatalf("cached view did not refresh active-path rules: %q", second.SystemPrompt)
	}
	if store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("rule refresh reloaded transcript: messages=%d after=%d", store.listMessageCalls, store.listAfterCalls)
	}
	if err := os.WriteFile(filepath.Join(ruleDirectory, "context.md"), []byte("updated child rule marker"), 0o600); err != nil {
		t.Fatal(err)
	}
	third, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "inspect child"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(third.SystemPrompt, "updated child rule marker") || strings.Contains(third.SystemPrompt, "\nchild rule marker") {
		t.Fatalf("cached view did not refresh changed rule content: %q", third.SystemPrompt)
	}
	if store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("rule content refresh reloaded transcript: messages=%d after=%d", store.listMessageCalls, store.listAfterCalls)
	}
}

func TestContextManagerCachedViewRefreshesLongTermMemory(t *testing.T) {
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
		SessionID: "session-memory", LifecycleKey: "session-memory:new", SystemPrompt: "system", LongTermMemory: "old memory",
		History: []conversation.Message{{Role: conversation.USER, Content: "first"}},
	}
	if _, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "first"}); err != nil {
		t.Fatal(err)
	}
	conversationContext.LongTermMemory = "new memory"
	view, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.SystemPrompt, "new memory") || strings.Contains(view.SystemPrompt, "old memory") {
		t.Fatalf("cached memory was stale: %q", view.SystemPrompt)
	}
	if store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("memory refresh reloaded transcript: messages=%d after=%d", store.listMessageCalls, store.listAfterCalls)
	}
}

func TestContextManagerCachedViewOffloadsNewLargeToolResult(t *testing.T) {
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
		SessionID: "session-offload", LifecycleKey: "session-offload:new", SystemPrompt: "system",
		History: []conversation.Message{{Role: conversation.USER, Content: "run tool"}},
	}
	if _, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "run tool"}); err != nil {
		t.Fatal(err)
	}
	largeResult := strings.Repeat("large tool output ", 300)
	conversationContext.History = append(conversationContext.History,
		conversation.Message{Role: conversation.ASSISTANT, ToolUses: []conversation.ToolUseBlock{{ToolUseID: "call-large", ToolName: "Bash"}}},
		conversation.Message{Role: conversation.TOOL, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "call-large", Content: largeResult}}},
	)
	view, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "run tool"})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.artifacts) != 1 || store.artifactContents[store.artifacts[0].ID] != largeResult {
		t.Fatalf("large cached result was not archived: artifacts=%+v", store.artifacts)
	}
	last := view.Messages[len(view.Messages)-1]
	if len(last.ToolResults) != 1 || !strings.Contains(last.ToolResults[0].Content, "[tool result archived]") {
		t.Fatalf("cached view retained full tool result: %+v", last.ToolResults)
	}
	if store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("offload reloaded transcript: messages=%d after=%d", store.listMessageCalls, store.listAfterCalls)
	}
}

func TestContextManagerCachedViewEvictsStaleToolResults(t *testing.T) {
	store := &countingConversationStore{}
	policy := DefaultPolicy()
	policy.ToolHistoryRatio = 0.05
	policy.SingleToolResultRatio = 0.40
	policy.ToolBatchRatio = 0.40
	manager, err := NewContextManager(ContextManagerConfig{
		Store: store, Estimator: ConservativeEstimator{},
		Model:  ModelContextSpec{ModelName: "test", ContextWindow: 10000, MaxOutputTokens: 1000},
		Policy: policy, Workspace: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldResult := strings.Repeat("old result ", 180)
	conversationContext := &ConversationContext{
		SessionID: "session-evict", LifecycleKey: "session-evict:new", SystemPrompt: "system",
		History: []conversation.Message{
			{Role: conversation.USER, Content: "first turn"},
			{Role: conversation.ASSISTANT, ToolUses: []conversation.ToolUseBlock{{ToolUseID: "call-old", ToolName: "Bash"}}},
			{Role: conversation.TOOL, ToolResults: []conversation.ToolResultBlock{{ToolUseID: "call-old", Content: oldResult}}},
			{Role: conversation.ASSISTANT, Content: "first done"},
		},
	}
	first, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "first turn", CurrentTurnID: "turn-000001"})
	if err != nil {
		t.Fatal(err)
	}
	if !viewContainsToolResult(first, oldResult) {
		t.Fatal("current-turn result was evicted before the next turn")
	}
	conversationContext.History = append(conversationContext.History, conversation.Message{Role: conversation.USER, Content: "second turn"})
	second, err := manager.Build(context.Background(), BuildInput{Context: conversationContext, CurrentRequest: "second turn", CurrentTurnID: "turn-000002"})
	if err != nil {
		t.Fatal(err)
	}
	if viewContainsToolResult(second, oldResult) || !viewContainsToolResult(second, "[stale tool result evicted") {
		t.Fatalf("stale result was not evicted: %+v", second.Messages)
	}
	if store.listMessageCalls != 2 || store.listAfterCalls != 0 {
		t.Fatalf("eviction reloaded transcript: messages=%d after=%d", store.listMessageCalls, store.listAfterCalls)
	}
}

func viewContainsToolResult(view *ContextView, fragment string) bool {
	for _, message := range view.Messages {
		for _, result := range message.ToolResults {
			if strings.Contains(result.Content, fragment) {
				return true
			}
		}
	}
	return false
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
