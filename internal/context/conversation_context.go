package contextmanager

import (
	"context"

	"FFCode/internal/conversation"
)

// ConversationContext is the complete unit of state consumed by an agent run.
// Session lifecycle and presentation remain the responsibility of the
// conversation layer; an agent only receives this context.
//
// The source session is deliberately private. It lets the boundary layer keep
// its in-memory view in sync after a run without exposing Session to agent.
type ConversationContext struct {
	SessionID      string
	LifecycleKey   string
	Workspace      string
	SystemPrompt   string
	LongTermMemory string
	History        []conversation.Message

	session *conversation.Session
}

// ContextFromSession creates an agent context from the current conversation
// state. Long-term memory is loaded here, once per turn, rather than as a side
// effect of every model request.
func ContextFromSession(ctx context.Context, session *conversation.Session) (*ConversationContext, error) {
	if session == nil {
		return nil, ErrInvalidIdentifier
	}
	if err := session.RefreshLongTermMemory(ctx); err != nil {
		return nil, err
	}
	return &ConversationContext{
		SessionID:      session.ID,
		LifecycleKey:   session.LifecycleKey(),
		Workspace:      session.Workspace,
		SystemPrompt:   session.SystemPrompt,
		LongTermMemory: session.LongTermMemory,
		History:        session.History,
		session:        session,
	}, nil
}

// Commit mirrors run changes to the owning session. It is intentionally a
// context-layer operation so callers never need to teach the agent about a
// Session aggregate.
func (c *ConversationContext) Commit() {
	if c == nil || c.session == nil {
		return
	}
	c.session.History = c.History
	c.session.LongTermMemory = c.LongTermMemory
}
