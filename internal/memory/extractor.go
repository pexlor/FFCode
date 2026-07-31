package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"FFCode/internal/conversation"
	"FFCode/internal/llm"
)

type ExtractRequest struct {
	SessionID      string
	Workspace      string
	SourceVersion  int
	TranscriptHash string
	Messages       []conversation.StoredMessage
}

type Extractor interface {
	Extract(context.Context, ExtractRequest) (RawMemory, error)
}

type LLMExtractor struct {
	Client        llm.LLMClient
	Model         string
	PromptVersion int
}

func (e LLMExtractor) Extract(ctx context.Context, request ExtractRequest) (RawMemory, error) {
	if e.Client == nil {
		return RawMemory{}, errors.New("memory extractor client is required")
	}
	payload, err := buildExtractionPayload(request.Messages)
	if err != nil {
		return RawMemory{}, err
	}
	input, err := json.Marshal(struct {
		SessionID      string                       `json:"session_id"`
		Workspace      string                       `json:"workspace"`
		SourceVersion  int                          `json:"source_version"`
		TranscriptHash string                       `json:"transcript_hash"`
		Messages       []conversation.StoredMessage `json:"messages"`
	}{request.SessionID, request.Workspace, request.SourceVersion, request.TranscriptHash, payload})
	if err != nil {
		return RawMemory{}, err
	}
	events, errs := e.Client.Stream(&llm.StreamRequest{Context: ctx, SystemPrompt: extractionPrompt, Messages: []conversation.Message{{Role: conversation.USER, Content: string(input)}}})
	var output strings.Builder
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return RawMemory{}, ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return RawMemory{}, err
			}
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch item := event.(type) {
			case llm.TextStream:
				output.WriteString(item.Text)
			case llm.ToolCallStart, llm.ToolCallStream, llm.ToolCallComplete:
				return RawMemory{}, errors.New("memory extractor attempted a tool call")
			}
		}
	}
	var raw RawMemory
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &raw); err != nil {
		return RawMemory{}, fmt.Errorf("decode memory extractor output: %w", err)
	}
	// Source metadata is authoritative runtime state. The model is never
	// allowed to redirect a memory to another session or transcript version.
	raw.SessionID = request.SessionID
	raw.Workspace = request.Workspace
	raw.SourceVersion = request.SourceVersion
	raw.TranscriptHash = request.TranscriptHash
	raw.PromptVersion = e.PromptVersion
	raw.GeneratedAt = time.Now()
	raw.ExtractorModel = e.Model
	for index := range raw.Categories {
		if strings.TrimSpace(raw.Categories[index].Scope) == "" {
			raw.Categories[index].Scope = "workspace"
		}
	}
	redactRawMemory(&raw)
	if len(raw.Categories) == 0 {
		raw.ID = ""
	} else {
		digest := sha256.Sum256([]byte(request.SessionID + "\x00" + request.TranscriptHash + fmt.Sprint(e.PromptVersion)))
		raw.ID = "raw-" + hex.EncodeToString(digest[:])[:24]
	}
	return raw, nil
}

func buildExtractionPayload(messages []conversation.StoredMessage) ([]conversation.StoredMessage, error) {
	const maxPayloadRunes = 48_000
	sanitized := make([]conversation.StoredMessage, 0, len(messages))
	for _, item := range messages {
		copy := item
		copy.Content = truncateRunes(redactText(copy.Content), 12_000)
		copy.Thinking = nil
		for index := range copy.ToolUses {
			copy.ToolUses[index].Arguments = sanitizeArguments(copy.ToolUses[index].Arguments)
		}
		for index := range copy.ToolResults {
			copy.ToolResults[index].Content = truncateRunes(redactText(copy.ToolResults[index].Content), 4_000)
		}
		sanitized = append(sanitized, copy)
	}

	// Budget from newest to oldest by complete Turn. This never cuts a tool
	// result away from the Turn that produced it.
	type turnGroup struct {
		id       string
		messages []conversation.StoredMessage
	}
	groups := make([]turnGroup, 0)
	for _, message := range sanitized {
		groupID := message.TurnID
		if groupID == "" {
			groupID = "unassigned"
		}
		if len(groups) == 0 || groups[len(groups)-1].id != groupID {
			groups = append(groups, turnGroup{id: groupID})
		}
		groups[len(groups)-1].messages = append(groups[len(groups)-1].messages, message)
	}
	start, used := len(groups), 0
	for index := len(groups) - 1; index >= 0; index-- {
		encoded, err := json.Marshal(groups[index].messages)
		if err != nil {
			return nil, err
		}
		size := len([]rune(string(encoded)))
		if used > 0 && used+size > maxPayloadRunes {
			break
		}
		start = index
		used += size
	}
	result := make([]conversation.StoredMessage, 0, len(sanitized))
	for _, group := range groups[start:] {
		result = append(result, group.messages...)
	}
	return result, nil
}

const extractionPrompt = `You extract only durable, evidence-backed memory from a coding session.
Return one JSON object with categories and session_summary. Runtime metadata is added by the caller.
Each category must use one of user_preference, correction, project_fact, reference and include
key (a stable lowercase slash path), scope (global, workspace, or session), content, confidence, and evidence with message_id,
turn_id, and a short verbatim quote. Use global scope only for an explicit cross-project user
preference. Do not include secrets, hidden reasoning, guesses, or temporary task state.
An empty categories array is correct when the session contains no durable memory.`

var (
	secretPattern   = regexp.MustCompile(`(?i)(sk-[a-z0-9]{12,}|(?:api[_-]?key|token|password|secret|authorization|cookie)\s*[:=]\s*[^\s,;]+)`)
	sensitiveMapKey = regexp.MustCompile(`(?i)^(api[_-]?key|token|password|secret|authorization|cookie)$`)
)

func redactRawMemory(raw *RawMemory) {
	raw.SessionSummary = redactText(raw.SessionSummary)
	filtered := raw.Categories[:0]
	for _, item := range raw.Categories {
		unsafe := secretPattern.MatchString(item.Content) || strings.Contains(item.Content, "[redacted]")
		for _, evidence := range item.Evidence {
			unsafe = unsafe || secretPattern.MatchString(evidence.Quote) || strings.Contains(evidence.Quote, "[redacted]")
		}
		if !unsafe {
			filtered = append(filtered, item)
		}
	}
	raw.Categories = filtered
}

func redactText(value string) string {
	return secretPattern.ReplaceAllString(value, "[redacted]")
}

func sanitizeArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	result := make(map[string]any, len(arguments))
	for key, value := range arguments {
		if sensitiveMapKey.MatchString(strings.TrimSpace(key)) {
			result[key] = "[redacted]"
			continue
		}
		result[key] = sanitizeValue(value)
	}
	return result
}

func sanitizeValue(value any) any {
	switch item := value.(type) {
	case string:
		return redactText(item)
	case map[string]any:
		return sanitizeArguments(item)
	case []any:
		result := make([]any, len(item))
		for index := range item {
			result[index] = sanitizeValue(item[index])
		}
		return result
	default:
		return value
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + " [truncated]"
	}
	return value
}
