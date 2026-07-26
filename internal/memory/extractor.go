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

	"MyCode/internal/conversation"
	"MyCode/internal/llm"
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
	if raw.SessionID == "" {
		raw.SessionID = request.SessionID
	}
	if raw.SourceVersion == 0 {
		raw.SourceVersion = request.SourceVersion
	}
	if raw.TranscriptHash == "" {
		raw.TranscriptHash = request.TranscriptHash
	}
	if raw.PromptVersion == 0 {
		raw.PromptVersion = e.PromptVersion
	}
	if raw.GeneratedAt.IsZero() {
		raw.GeneratedAt = time.Now()
	}
	if raw.ID == "" {
		digest := sha256.Sum256([]byte(request.SessionID + "\x00" + request.TranscriptHash + fmt.Sprint(e.PromptVersion)))
		raw.ID = "raw-" + hex.EncodeToString(digest[:])[:24]
	}
	redactRawMemory(&raw)
	return raw, nil
}

func buildExtractionPayload(messages []conversation.StoredMessage) ([]conversation.StoredMessage, error) {
	result := make([]conversation.StoredMessage, 0, len(messages))
	for _, item := range messages {
		copy := item
		copy.Content = truncateRunes(copy.Content, 12_000)
		for index := range copy.ToolResults {
			copy.ToolResults[index].Content = truncateRunes(copy.ToolResults[index].Content, 4_000)
		}
		result = append(result, copy)
	}
	return result, nil
}

const extractionPrompt = `You extract only durable, evidence-backed memory from a coding session.
Return one JSON object with id, session_id, source_version, transcript_hash, prompt_version,
categories, and session_summary. Each category must use one of user_preference, correction,
project_fact, reference and include key, content, confidence, and evidence with message_id,
turn_id, and a short quote. Do not include secrets, hidden reasoning, guesses, or temporary state.
An empty categories array is correct when the session contains no durable memory.`

var secretPattern = regexp.MustCompile(`(?i)(sk-[a-z0-9]{12,}|(?:api[_-]?key|token|password)\s*[:=]\s*[^\s,;]+)`)

func redactRawMemory(raw *RawMemory) {
	raw.SessionSummary = secretPattern.ReplaceAllString(raw.SessionSummary, "[redacted]")
	for index := range raw.Categories {
		raw.Categories[index].Content = secretPattern.ReplaceAllString(raw.Categories[index].Content, "[redacted]")
		for evidenceIndex := range raw.Categories[index].Evidence {
			raw.Categories[index].Evidence[evidenceIndex].Quote = secretPattern.ReplaceAllString(raw.Categories[index].Evidence[evidenceIndex].Quote, "[redacted]")
		}
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		return string(runes[:limit]) + " [truncated]"
	}
	return value
}
