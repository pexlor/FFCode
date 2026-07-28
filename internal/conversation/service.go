package conversation

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"MyCode/internal/hook"
)

const (
	DefaultTitle   = "未命名会话"
	MaxTitleRunes  = 80
	AutoTitleRunes = 40
)

var (
	ErrSessionNotFound       = errors.New("session not found")
	ErrAmbiguousSessionID    = errors.New("ambiguous session id")
	ErrCurrentSessionDelete  = errors.New("cannot delete current session")
	ErrInvalidSessionTitle   = errors.New("invalid session title")
	ErrUserPromptRejected    = errors.New("user prompt rejected by hook")
	ErrSessionStartRejected  = errors.New("session start rejected by hook")
	sessionLifecycleSequence atomic.Uint64
)

type Store interface {
	CreateSession(context.Context, SessionMetadata) error
	GetSession(context.Context, string) (SessionMetadata, error)
	ListSessions(context.Context, string, int) ([]SessionMetadata, error)
	RenameSession(context.Context, string, string) error
	DeleteSession(context.Context, string) error
	ListMessages(context.Context, string) ([]StoredMessage, error)
}

type SessionContext struct {
	SystemPrompt string
	Memory       MemoryProvider
	UseMemory    bool
	Hooks        *hook.Dispatcher
}

type Service struct {
	store     Store
	workspace string
	context   SessionContext
	current   *Session
}

func NewService(store Store, workspace string, sessionContext SessionContext) (*Service, error) {
	if store == nil || strings.TrimSpace(workspace) == "" {
		return nil, errors.New("session store and workspace are required")
	}
	s := &Service{store: store, workspace: workspace, context: sessionContext}
	if _, err := s.New(context.Background(), ""); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Service) New(ctx context.Context, title string) (*Session, error) {
	title, explicit, err := normalizeOptionalTitle(title)
	if err != nil {
		return nil, err
	}
	id, err := newSessionID()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	next := &Session{
		ID: id, Title: title, Workspace: s.workspace, CreatedAt: now, UpdatedAt: now,
		ExplicitTitle: explicit, SystemPrompt: s.context.SystemPrompt,
		memoryProvider: s.context.Memory, useMemory: s.context.UseMemory,
		lifecycleKey: newSessionLifecycleKey(id),
	}
	if err := s.dispatchSessionStart(ctx, next, "new"); err != nil {
		return nil, err
	}
	s.current = next
	return next, nil
}

func (s *Service) Current() *Session {
	return s.current
}

// SetHookDispatcher replaces the shared lifecycle dispatcher used by future
// session transitions and prompt submissions.
func (s *Service) SetHookDispatcher(dispatcher *hook.Dispatcher) {
	s.context.Hooks = dispatcher
}

func (s *Service) AddUserMessage(ctx context.Context, content string) error {
	if s.current == nil {
		return errors.New("current session is not initialized")
	}
	promptHookKey := ""
	if s.context.Hooks != nil {
		promptHookKey = promptHookKeyForSession(s.current)
	}
	content, err := s.dispatchUserPrompt(ctx, content)
	if err != nil {
		return err
	}
	if !s.current.Persisted {
		if !s.current.ExplicitTitle {
			s.current.Title = autoTitle(content)
		}
		metadata := SessionMetadata{ID: s.current.ID, Title: s.current.Title, Workspace: s.workspace, CreatedAt: s.current.CreatedAt, UpdatedAt: time.Now()}
		if err := s.store.CreateSession(ctx, metadata); err != nil {
			if promptHookKey != "" {
				s.context.Hooks.ResetOnce(hook.EventUserPromptSubmit, promptHookKey)
			}
			return fmt.Errorf("create session %s: %w", s.current.ID, err)
		}
		s.current.Persisted = true
	}
	s.current.AddText(content)
	s.current.UpdatedAt = time.Now()
	return nil
}

func (s *Service) List(ctx context.Context, limit int) ([]SessionMetadata, error) {
	items, err := s.store.ListSessions(ctx, s.workspace, limit)
	if err != nil {
		return nil, err
	}
	if s.current != nil && !s.current.Persisted {
		current := SessionMetadata{ID: s.current.ID, Title: s.current.Title, Workspace: s.workspace, CreatedAt: s.current.CreatedAt, UpdatedAt: s.current.UpdatedAt, MessageCount: len(s.current.History)}
		items = append([]SessionMetadata{current}, items...)
	}
	return items, nil
}

func (s *Service) Resume(ctx context.Context, idOrPrefix string) (*Session, error) {
	metadata, err := s.resolve(ctx, idOrPrefix)
	if err != nil {
		return nil, err
	}
	if s.current != nil && metadata.ID == s.current.ID {
		return s.current, nil
	}
	stored, err := s.store.ListMessages(ctx, metadata.ID)
	if err != nil {
		return nil, fmt.Errorf("load session %s: %w", metadata.ID, err)
	}
	recovered := completeMessages(stored)
	history := restoreMessages(recovered)
	if len(stored) > len(recovered) || len(history) > 0 {
		history = append(history, Message{Role: USER, Content: fmt.Sprintf("[context boundary: resumed session last activity %s; read current files for exact state]", metadata.UpdatedAt.Local().Format(time.RFC3339))})
	}
	next := &Session{
		ID: metadata.ID, Title: metadata.Title, Workspace: metadata.Workspace,
		CreatedAt: metadata.CreatedAt, UpdatedAt: metadata.UpdatedAt, Persisted: true,
		ExplicitTitle: metadata.Title != "" && metadata.Title != DefaultTitle,
		SystemPrompt:  s.context.SystemPrompt, History: history,
		memoryProvider: s.context.Memory, useMemory: s.context.UseMemory,
		lifecycleKey: newSessionLifecycleKey(metadata.ID),
	}
	if err := s.dispatchSessionStart(ctx, next, "resume"); err != nil {
		return nil, err
	}
	s.current = next
	return next, nil
}

func (s *Service) dispatchSessionStart(ctx context.Context, session *Session, reason string) error {
	if s == nil || s.context.Hooks == nil || session == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	lifecycleKey := session.LifecycleKey()
	result, err := s.context.Hooks.DispatchOnce(ctx, hook.EventSessionStart, lifecycleKey, hook.Input{
		SessionID: session.ID,
		Workspace: session.Workspace,
		Reason:    reason,
		Metadata: map[string]any{
			"title":     session.Title,
			"persisted": session.Persisted,
		},
	})
	if err != nil {
		return fmt.Errorf("session_start hook: %w", err)
	}
	if result.Blocked {
		s.context.Hooks.ResetOnce(hook.EventSessionStart, lifecycleKey)
		return fmt.Errorf("%w: %s", ErrSessionStartRejected, hookReason(result.Reason))
	}
	return nil
}

func newSessionLifecycleKey(sessionID string) string {
	return fmt.Sprintf("%s:%d", sessionID, sessionLifecycleSequence.Add(1))
}

func (s *Service) dispatchUserPrompt(ctx context.Context, content string) (string, error) {
	if s == nil || s.context.Hooks == nil || s.current == nil {
		return content, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	input := hook.Input{
		SessionID:  s.current.ID,
		Workspace:  s.current.Workspace,
		UserPrompt: content,
		Prompt:     content,
	}
	key := promptHookKey(s.current.ID, nextUserPromptOrdinal(s.current.History))
	result, err := s.context.Hooks.DispatchOnce(
		hook.WithInput(ctx, input),
		hook.EventUserPromptSubmit,
		key,
		input,
	)
	if err != nil {
		return "", fmt.Errorf("user_prompt_submit hook: %w", err)
	}
	if result.Blocked {
		s.context.Hooks.ResetOnce(hook.EventUserPromptSubmit, key)
		return "", fmt.Errorf("%w: %s", ErrUserPromptRejected, hookReason(result.Reason))
	}
	for _, key := range []string{"user_prompt", "prompt"} {
		if updated, ok := result.UpdatedInput[key].(string); ok {
			return updated, nil
		}
	}
	return content, nil
}

func nextUserPromptOrdinal(history []Message) int {
	ordinal := 1
	for _, message := range history {
		if message.Role == USER {
			ordinal++
		}
	}
	return ordinal
}

func promptHookKey(sessionID string, ordinal int) string {
	return fmt.Sprintf("%s:%d", sessionID, ordinal)
}

func promptHookKeyForSession(session *Session) string {
	if session == nil {
		return ""
	}
	return promptHookKey(session.ID, nextUserPromptOrdinal(session.History))
}

func hookReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "hook denied operation"
	}
	return reason
}

func (s *Service) Delete(ctx context.Context, idOrPrefix string) error {
	metadata, err := s.resolve(ctx, idOrPrefix)
	if err != nil {
		return err
	}
	if s.current != nil && metadata.ID == s.current.ID {
		return ErrCurrentSessionDelete
	}
	return s.store.DeleteSession(ctx, metadata.ID)
}

func (s *Service) Rename(ctx context.Context, title string) error {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > MaxTitleRunes {
		return ErrInvalidSessionTitle
	}
	if s.current.Persisted {
		if err := s.store.RenameSession(ctx, s.current.ID, title); err != nil {
			return fmt.Errorf("rename session %s: %w", s.current.ID, err)
		}
	}
	s.current.Title = title
	s.current.ExplicitTitle = true
	s.current.UpdatedAt = time.Now()
	return nil
}

func (s *Service) resolve(ctx context.Context, prefix string) (SessionMetadata, error) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return SessionMetadata{}, ErrSessionNotFound
	}
	if exact, err := s.store.GetSession(ctx, prefix); err == nil {
		if exact.Workspace != s.workspace {
			return SessionMetadata{}, ErrSessionNotFound
		}
		return exact, nil
	} else if !errors.Is(err, ErrStoreSessionNotFound) && !errors.Is(err, ErrInvalidIdentifier) {
		return SessionMetadata{}, err
	}
	items, err := s.store.ListSessions(ctx, s.workspace, 0)
	if err != nil {
		return SessionMetadata{}, err
	}
	var matches []SessionMetadata
	for _, item := range items {
		if strings.HasPrefix(item.ID, prefix) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 0 {
		return SessionMetadata{}, ErrSessionNotFound
	}
	if len(matches) > 1 {
		return SessionMetadata{}, ErrAmbiguousSessionID
	}
	return matches[0], nil
}

func restoreMessages(stored []StoredMessage) []Message {
	result := make([]Message, 0, len(stored))
	for _, item := range stored {
		converted := Message{Role: item.Role, Content: item.Content}
		for _, thinking := range item.Thinking {
			converted.ThinkingBlocks = append(converted.ThinkingBlocks, ThinkingBlock{Thinking: thinking.Thinking, Signature: thinking.Signature})
		}
		for _, use := range item.ToolUses {
			converted.ToolUses = append(converted.ToolUses, ToolUseBlock{ToolUseID: use.ToolUseID, ToolName: use.ToolName, Arguments: use.Arguments})
		}
		for _, toolResult := range item.ToolResults {
			converted.ToolResults = append(converted.ToolResults, ToolResultBlock{ToolUseID: toolResult.ToolUseID, Content: toolResult.Content, IsError: toolResult.IsError})
		}
		result = append(result, converted)
	}
	return result
}

func completeMessages(stored []StoredMessage) []StoredMessage {
	lastComplete := -1
	for index, item := range stored {
		if item.TurnStatus == TurnComplete {
			lastComplete = index
		}
	}
	if lastComplete < 0 {
		return nil
	}
	return append([]StoredMessage(nil), stored[:lastComplete+1]...)
}

func normalizeOptionalTitle(title string) (string, bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return DefaultTitle, false, nil
	}
	if utf8.RuneCountInString(title) > MaxTitleRunes {
		return "", false, ErrInvalidSessionTitle
	}
	return title, true, nil
}

func autoTitle(content string) string {
	line := content
	if index := strings.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	line = strings.Join(strings.Fields(line), " ")
	if line == "" {
		return DefaultTitle
	}
	runes := []rune(line)
	if len(runes) > AutoTitleRunes {
		runes = runes[:AutoTitleRunes]
	}
	return string(runes)
}

func newSessionID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return "session-" + hex.EncodeToString(bytes), nil
}
