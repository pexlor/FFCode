package memory

import (
	"fmt"
	"regexp"
	"strings"

	"FFCode/internal/conversation"
)

var memoryKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]{1,159}$`)

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
		if !validKind(item.Kind) || !memoryKeyPattern.MatchString(item.Key) || !validScope(item.Scope) || strings.TrimSpace(item.Content) == "" || item.Confidence < 0 || item.Confidence > 1 || len(item.Evidence) == 0 {
			return fmt.Errorf("%w: invalid item %d", ErrInvalidMemory, index)
		}
		if item.Scope == "global" && item.Kind != UserPreference && item.Kind != Correction {
			return fmt.Errorf("%w: global item %q must be a user preference or correction", ErrInvalidMemory, item.Key)
		}
		if seenKeys[item.Key] {
			return fmt.Errorf("%w: duplicate key %q", ErrInvalidMemory, item.Key)
		}
		seenKeys[item.Key] = true
		hasUserEvidence := false
		hasDirectProjectEvidence := false
		for _, evidence := range item.Evidence {
			message, ok := byID[evidence.MessageID]
			quote := normalizeEvidenceText(evidence.Quote)
			if !ok || message.TurnID != evidence.TurnID || quote == "" || !strings.Contains(normalizeEvidenceText(messageEvidenceText(message)), quote) {
				return fmt.Errorf("%w: item %q has invalid evidence", ErrInvalidMemory, item.Key)
			}
			if message.Role == conversation.USER {
				hasUserEvidence = true
			}
			if message.Role == conversation.USER || message.Role == conversation.TOOL {
				hasDirectProjectEvidence = true
			}
		}
		if (item.Kind == UserPreference || item.Kind == Correction) && !hasUserEvidence {
			return fmt.Errorf("%w: item %q requires user evidence", ErrInvalidMemory, item.Key)
		}
		if item.Kind == ProjectFact && !hasDirectProjectEvidence {
			return fmt.Errorf("%w: item %q requires direct user or tool evidence", ErrInvalidMemory, item.Key)
		}
	}
	return nil
}

func validScope(scope string) bool {
	switch strings.TrimSpace(scope) {
	case "", "global", "workspace", "session":
		return true
	default:
		return false
	}
}

func messageEvidenceText(message conversation.StoredMessage) string {
	var builder strings.Builder
	builder.WriteString(message.Content)
	for _, result := range message.ToolResults {
		builder.WriteByte('\n')
		builder.WriteString(result.Content)
	}
	return builder.String()
}

func normalizeEvidenceText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func validKind(kind MemoryKind) bool {
	switch kind {
	case UserPreference, Correction, ProjectFact, Reference:
		return true
	default:
		return false
	}
}
