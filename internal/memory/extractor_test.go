package memory

import (
	"context"
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
	extractor := LLMExtractor{Client: fakeLLM{events: []llm.StreamEvent{llm.TextStream{Text: `{"id":"raw-1","session_id":"session-1","source_version":1,"transcript_hash":"hash","prompt_version":1,"categories":[]}`}}}, Model: "test", PromptVersion: 1}
	raw, err := extractor.Extract(context.Background(), ExtractRequest{SessionID: "session-1", TranscriptHash: "hash", SourceVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	if raw.ID != "raw-1" || raw.SessionID != "session-1" {
		t.Fatalf("unexpected raw memory: %+v", raw)
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
