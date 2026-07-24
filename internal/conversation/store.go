package conversation

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

var (
	ErrInvalidIdentifier    = errors.New("invalid conversation identifier")
	ErrStoreSessionNotFound = errors.New("session not found")
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func ValidIdentifier(value string) bool {
	return value != "" && !strings.Contains(value, "..") && identifierPattern.MatchString(value)
}

// Repository describes durable session and transcript operations required by
// the conversation service. Concrete file/database implementations live in
// infrastructure packages.
type Repository interface {
	CreateSession(context.Context, SessionMetadata) error
	GetSession(context.Context, string) (SessionMetadata, error)
	ListSessions(context.Context, string, int) ([]SessionMetadata, error)
	RenameSession(context.Context, string, string) error
	DeleteSession(context.Context, string) error
	AppendMessage(context.Context, StoredMessage) error
	ListMessages(context.Context, string) ([]StoredMessage, error)
	ListMessagesAfter(context.Context, string, string) ([]StoredMessage, error)
}
