package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"FFCode/internal/conversation"
	"FFCode/internal/llm"
)

type fakeLLM struct {
	events []llm.StreamEvent
	errs   []error
}

func (f fakeLLM) Stream(_ *llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	events := make(chan llm.StreamEvent, len(f.events))
	errs := make(chan error, len(f.errs))
	for _, event := range f.events {
		events <- event
	}
	for _, err := range f.errs {
		errs <- err
	}
	close(events)
	close(errs)
	return events, errs
}

func TestLLMExtractorParsesStructuredMemory(t *testing.T) {
	extractor := LLMExtractor{Client: fakeLLM{events: []llm.StreamEvent{llm.TextStream{Text: `{"id":"model-controlled","session_id":"wrong","categories":[{"key":"reference/project/tests","kind":"reference","content":"Run go test.","confidence":0.9,"evidence":[{"message_id":"m1","turn_id":"t1","quote":"go test"}]}]}`}}}, Model: "extract-model", PromptVersion: 1}
	raw, err := extractor.Extract(context.Background(), ExtractRequest{SessionID: "session-1", Workspace: "/project", TranscriptHash: "hash", SourceVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw.ID, "raw-") || raw.ID == "model-controlled" || raw.SessionID != "session-1" || raw.Workspace != "/project" || raw.ExtractorModel != "extract-model" || raw.Categories[0].Scope != "workspace" {
		t.Fatalf("unexpected raw memory: %+v", raw)
	}
}

func TestLLMExtractorTreatsEmptyCategoriesAsNoOutput(t *testing.T) {
	extractor := LLMExtractor{Client: fakeLLM{events: []llm.StreamEvent{llm.TextStream{Text: `{"id":"model-id","categories":[]}`}}}, Model: "test", PromptVersion: 1}
	raw, err := extractor.Extract(context.Background(), ExtractRequest{SessionID: "session-1", TranscriptHash: "hash", SourceVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if raw.ID != "" || len(raw.Categories) != 0 {
		t.Fatalf("empty extraction must not produce raw memory: %+v", raw)
	}
}

func TestLLMExtractorRejectsToolCalls(t *testing.T) {
	extractor := LLMExtractor{Client: fakeLLM{events: []llm.StreamEvent{llm.ToolCallStart{ToolID: "tool-1", ToolName: "exec"}}}, Model: "test", PromptVersion: 1}
	if _, err := extractor.Extract(context.Background(), ExtractRequest{}); err == nil {
		t.Fatal("expected tool call to be rejected")
	}
}

func TestBuildExtractionPayloadKeepsRelevantMessages(t *testing.T) {
	payload, err := buildExtractionPayload([]conversation.StoredMessage{{ID: "m1", Role: conversation.USER, Content: "remember this"}, {ID: "m2", Role: conversation.ASSISTANT, Content: "done"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) != 2 || payload[0].ID != "m1" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

func TestBuildExtractionPayloadRemovesThinkingAndRedactsSecrets(t *testing.T) {
	payload, err := buildExtractionPayload([]conversation.StoredMessage{{
		ID: "m1", TurnID: "t1", Role: conversation.USER, Content: "password=plain-secret",
		Thinking:    []conversation.StoredThinkingBlock{{Thinking: "hidden-chain"}},
		ToolUses:    []conversation.StoredToolUse{{ToolName: "curl", Arguments: map[string]any{"token": "tool-secret", "url": "https://example.com"}}},
		ToolResults: []conversation.StoredToolResult{{Content: "sk-abcdefghijklmnop"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"plain-secret", "tool-secret", "sk-abcdefghijklmnop", "hidden-chain"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("payload leaked %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[redacted]") {
		t.Fatalf("payload did not retain redaction marker: %s", text)
	}
}

func TestBuildExtractionPayloadDropsOldTurnsAtBudgetBoundary(t *testing.T) {
	var messages []conversation.StoredMessage
	for index := 1; index <= 5; index++ {
		messages = append(messages, conversation.StoredMessage{ID: string(rune('a' + index)), TurnID: string(rune('a' + index)), Role: conversation.USER, Content: strings.Repeat(string(rune('a'+index)), 12_000)})
	}
	payload, err := buildExtractionPayload(messages)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) >= len(messages) || payload[len(payload)-1].TurnID != messages[len(messages)-1].TurnID {
		t.Fatalf("payload should retain newest complete turns within budget: got %d messages", len(payload))
	}
}
