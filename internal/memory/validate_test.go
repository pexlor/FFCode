package memory

import (
	"testing"
	"time"

	"FFCode/internal/conversation"
)

func TestValidateRawMemoryRequiresValidEvidence(t *testing.T) {
	messages := []conversation.StoredMessage{{ID: "message-000001", SessionID: "session-1", TurnID: "turn-1", Role: conversation.USER, Content: "Use rg for searches."}}
	memory := RawMemory{
		ID: "raw-1", SessionID: "session-1", Workspace: "/tmp/project", SourceVersion: 1,
		TranscriptHash: "abc", GeneratedAt: time.Now(), ExtractorModel: "test", PromptVersion: 1,
		Categories: []MemoryItem{{Key: "user_preference/project/search", Kind: UserPreference, Content: "Use rg for searches.", Confidence: 0.9, Evidence: []Evidence{{MessageID: "missing", TurnID: "turn-1", Quote: "Use rg"}}}},
	}

	if err := ValidateRawMemory(memory, messages); err == nil {
		t.Fatal("expected invalid evidence to be rejected")
	}
	memory.Categories[0].Evidence[0].MessageID = "message-000001"
	if err := ValidateRawMemory(memory, messages); err != nil {
		t.Fatalf("expected valid memory: %v", err)
	}
}

func TestValidateRawMemoryRejectsAssistantOnlyPreference(t *testing.T) {
	messages := []conversation.StoredMessage{{ID: "message-000001", SessionID: "session-1", TurnID: "turn-1", Role: conversation.ASSISTANT, Content: "The user prefers tabs."}}
	memory := RawMemory{
		ID: "raw-1", SessionID: "session-1", Workspace: "/tmp/project", SourceVersion: 1,
		TranscriptHash: "abc", GeneratedAt: time.Now(), PromptVersion: 1,
		Categories: []MemoryItem{{Key: "user_preference/project/tabs", Kind: UserPreference, Content: "Use tabs.", Confidence: 0.8, Evidence: []Evidence{{MessageID: "message-000001", TurnID: "turn-1", Quote: "prefers tabs"}}}},
	}

	if err := ValidateRawMemory(memory, messages); err == nil {
		t.Fatal("expected assistant-only preference to be rejected")
	}
}

func TestValidateRawMemoryRejectsInventedEvidenceQuote(t *testing.T) {
	messages := []conversation.StoredMessage{{ID: "message-000001", TurnID: "turn-1", Role: conversation.USER, Content: "Use rg for searches."}}
	raw := RawMemory{
		ID: "raw-1", SessionID: "session-1", SourceVersion: 1, TranscriptHash: "abc", GeneratedAt: time.Now(), PromptVersion: 1,
		Categories: []MemoryItem{{Key: "user_preference/project/search", Kind: UserPreference, Scope: "workspace", Content: "Use rg.", Confidence: 0.9, Evidence: []Evidence{{MessageID: "message-000001", TurnID: "turn-1", Quote: "Use grep"}}}},
	}
	if err := ValidateRawMemory(raw, messages); err == nil {
		t.Fatal("invented quote was accepted")
	}
}

func TestValidateRawMemoryRejectsAssistantOnlyProjectFact(t *testing.T) {
	messages := []conversation.StoredMessage{{ID: "message-000001", TurnID: "turn-1", Role: conversation.ASSISTANT, Content: "The config is config.yaml."}}
	raw := RawMemory{
		ID: "raw-1", SessionID: "session-1", SourceVersion: 1, TranscriptHash: "abc", GeneratedAt: time.Now(), PromptVersion: 1,
		Categories: []MemoryItem{{Key: "project_fact/project/config", Kind: ProjectFact, Scope: "workspace", Content: "Config is config.yaml.", Confidence: 0.8, Evidence: []Evidence{{MessageID: "message-000001", TurnID: "turn-1", Quote: "config is config.yaml"}}}},
	}
	if err := ValidateRawMemory(raw, messages); err == nil {
		t.Fatal("assistant-only project fact was accepted")
	}
}
