package memory

import (
	"fmt"
	"strings"

	"MyCode/internal/conversation"
)

func ValidateRawMemory(raw RawMemory, messages []conversation.StoredMessage) error {
	if strings.TrimSpace(raw.ID) == "" || strings.TrimSpace(raw.SessionID) == "" || raw.SourceVersion <= 0 || strings.TrimSpace(raw.TranscriptHash) == "" || raw.PromptVersion <= 0 {
		return fmt.Errorf("%w: required raw memory metadata is missing", ErrInvalidMemory)
	}
	byID := make(map[string]conversation.StoredMessage, len(messages))
	for _, message := range messages {
		byID[message.ID] = message
	}
	seenKeys := make(map[string]bool, len(raw.Categories))
	for index, item := range raw.Categories {
		if !validKind(item.Kind) || strings.TrimSpace(item.Key) == "" || strings.TrimSpace(item.Content) == "" || item.Confidence < 0 || item.Confidence > 1 || len(item.Evidence) == 0 {
			return fmt.Errorf("%w: invalid item %d", ErrInvalidMemory, index)
		}
		if seenKeys[item.Key] {
			return fmt.Errorf("%w: duplicate key %q", ErrInvalidMemory, item.Key)
		}
		seenKeys[item.Key] = true
		for _, evidence := range item.Evidence {
			message, ok := byID[evidence.MessageID]
			if !ok || message.TurnID != evidence.TurnID || strings.TrimSpace(evidence.Quote) == "" {
				return fmt.Errorf("%w: item %q has invalid evidence", ErrInvalidMemory, item.Key)
			}
			if (item.Kind == UserPreference || item.Kind == Correction) && message.Role != conversation.USER {
				return fmt.Errorf("%w: item %q requires user evidence", ErrInvalidMemory, item.Key)
			}
		}
	}
	return nil
}

func validKind(kind MemoryKind) bool {
	switch kind {
	case UserPreference, Correction, ProjectFact, Reference:
		return true
	default:
		return false
	}
}
