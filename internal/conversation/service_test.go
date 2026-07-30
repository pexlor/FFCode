package conversation

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"FFCode/internal/hook"
)

type serviceStore struct {
	metadata  SessionMetadata
	messages  []StoredMessage
	createErr error
}

type staticMemoryProvider struct {
	summary string
	calls   int
}

func (p *staticMemoryProvider) Summary(context.Context) (string, error) {
	p.calls++
	return p.summary, nil
}

func (s *serviceStore) CreateSession(_ context.Context, metadata SessionMetadata) error {
	if s.createErr != nil {
		return s.createErr
	}
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
	service, err := NewService(store, "/workspace", SessionContext{SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Resume(context.Background(), "session-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(current.History) != 2 {
		t.Fatalf("expected complete history plus boundary, got %+v", current.History)
	}
	if current.History[0].Content != "first" || !strings.Contains(current.History[1].Content, "context boundary") {
		t.Fatalf("unexpected resumed history: %+v", current.History)
	}
}

func TestResumeAcceptsDisplayedShortSessionID(t *testing.T) {
	store := &serviceStore{metadata: SessionMetadata{
		ID:        "session-cc06be1e0123456789abcdef01234567",
		Title:     "test",
		Workspace: "/workspace",
		CreatedAt: time.Now().Add(-time.Hour),
		UpdatedAt: time.Now(),
	}}
	service, err := NewService(store, "/workspace", SessionContext{SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}

	current, err := service.Resume(context.Background(), "cc06be1e")
	if err != nil {
		t.Fatal(err)
	}
	if current.ID != store.metadata.ID {
		t.Fatalf("resumed session %q, want %q", current.ID, store.metadata.ID)
	}
}

func TestSessionOwnsAndRefreshesLongTermMemory(t *testing.T) {
	provider := &staticMemoryProvider{summary: "prefer rg"}
	service, err := NewService(&serviceStore{}, "/workspace", SessionContext{
		SystemPrompt: "system",
		Memory:       provider,
		UseMemory:    true,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := service.Current()
	if err := session.RefreshLongTermMemory(context.Background()); err != nil {
		t.Fatal(err)
	}
	if session.SystemPrompt != "system" || session.LongTermMemory != "prefer rg" || provider.calls != 1 {
		t.Fatalf("unexpected session context: %+v, calls=%d", session, provider.calls)
	}
}

func TestResumeRestoresThinkingBlocks(t *testing.T) {
	store := &serviceStore{
		metadata: SessionMetadata{ID: "session-thinking", Title: "test", Workspace: "/workspace", CreatedAt: time.Now(), UpdatedAt: time.Now()},
		messages: []StoredMessage{{
			ID: "message-1", SessionID: "session-thinking", TurnID: "turn-1", Role: ASSISTANT,
			Thinking: []StoredThinkingBlock{{Thinking: "consider options", Signature: "signed"}}, TurnStatus: TurnComplete,
		}},
	}
	service, err := NewService(store, "/workspace", SessionContext{SystemPrompt: "system"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := service.Resume(context.Background(), "session-thinking")
	if err != nil {
		t.Fatal(err)
	}
	got := session.History[0].ThinkingBlocks
	if len(got) != 1 || got[0].Thinking != "consider options" || got[0].Signature != "signed" {
		t.Fatalf("thinking blocks = %#v", got)
	}
}

func TestSessionStartHookRunsOncePerSession(t *testing.T) {
	dispatcher := hook.New(hook.DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventSessionStart, func(_ context.Context, input hook.Input) (hook.Output, error) {
		if input.SessionID == "" || input.Workspace != "/workspace" {
			t.Fatalf("input = %+v", input)
		}
		calls.Add(1)
		return hook.Output{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewService(&serviceStore{}, "/workspace", SessionContext{Hooks: dispatcher})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.dispatchSessionStart(context.Background(), service.Current(), "duplicate"); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("session_start calls = %d, want 1", got)
	}
}

func TestUserPromptHookTransformsBeforePersistence(t *testing.T) {
	store := &serviceStore{}
	service, err := NewService(store, "/workspace", SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := hook.New(hook.DefaultConfig())
	if err := dispatcher.Register(hook.EventUserPromptSubmit, func(_ context.Context, input hook.Input) (hook.Output, error) {
		if input.UserPrompt != "original" {
			t.Fatalf("input = %+v", input)
		}
		return hook.Output{UpdatedInput: map[string]any{"prompt": "rewritten"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	service.SetHookDispatcher(dispatcher)
	if err := service.AddUserMessage(context.Background(), "original"); err != nil {
		t.Fatal(err)
	}
	current := service.Current()
	if len(current.History) != 1 || current.History[0].Content != "rewritten" || current.Title != "rewritten" {
		t.Fatalf("current session = %+v", current)
	}
	if store.metadata.Title != "rewritten" {
		t.Fatalf("stored metadata = %+v", store.metadata)
	}
}

func TestRejectedUserPromptIsNotPersisted(t *testing.T) {
	store := &serviceStore{}
	service, err := NewService(store, "/workspace", SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := hook.New(hook.DefaultConfig())
	if err := dispatcher.Register(hook.EventUserPromptSubmit, func(hook.Input) hook.Output {
		return hook.Output{Decision: hook.DecisionDeny, Reason: "blocked"}
	}); err != nil {
		t.Fatal(err)
	}
	service.SetHookDispatcher(dispatcher)
	err = service.AddUserMessage(context.Background(), "secret")
	if !errors.Is(err, ErrUserPromptRejected) {
		t.Fatalf("error = %v", err)
	}
	if service.Current().Persisted || len(service.Current().History) != 0 || store.metadata.ID != "" {
		t.Fatalf("rejected prompt changed state: session=%+v metadata=%+v", service.Current(), store.metadata)
	}
}

func TestRejectedPromptDoesNotPoisonNextSubmission(t *testing.T) {
	store := &serviceStore{}
	service, err := NewService(store, "/workspace", SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := hook.New(hook.DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventUserPromptSubmit, func(hook.Input) hook.Output {
		if calls.Add(1) == 1 {
			return hook.Output{Decision: hook.DecisionDeny, Reason: "first rejected"}
		}
		return hook.Output{}
	}); err != nil {
		t.Fatal(err)
	}
	service.SetHookDispatcher(dispatcher)
	if err := service.AddUserMessage(context.Background(), "first"); !errors.Is(err, ErrUserPromptRejected) {
		t.Fatalf("first error = %v", err)
	}
	if err := service.AddUserMessage(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || service.Current().History[0].Content != "second" {
		t.Fatalf("calls=%d history=%+v", calls.Load(), service.Current().History)
	}
}

func TestPromptHookIsReevaluatedAfterSessionPersistenceFailure(t *testing.T) {
	store := &serviceStore{createErr: errors.New("disk unavailable")}
	service, err := NewService(store, "/workspace", SessionContext{})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := hook.New(hook.DefaultConfig())
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventUserPromptSubmit, func(input hook.Input) hook.Output {
		calls.Add(1)
		return hook.Output{UpdatedInput: map[string]any{"prompt": "checked:" + input.Prompt}}
	}); err != nil {
		t.Fatal(err)
	}
	service.SetHookDispatcher(dispatcher)

	if err := service.AddUserMessage(context.Background(), "first"); err == nil {
		t.Fatal("first persistence unexpectedly succeeded")
	}
	store.createErr = nil
	if err := service.AddUserMessage(context.Background(), "second"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || service.Current().History[0].Content != "checked:second" {
		t.Fatalf("calls=%d history=%+v", calls.Load(), service.Current().History)
	}
}
