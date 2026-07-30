package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"FFCode/internal/conversation"
	"FFCode/internal/llm"
)

type ConsolidateRequest struct {
	Previous *MemorySnapshot
	Inputs   []RawMemory
}

type Consolidator interface {
	Consolidate(context.Context, ConsolidateRequest) (MemorySnapshot, error)
}

type DeterministicConsolidator struct{}

func (DeterministicConsolidator) Consolidate(ctx context.Context, request ConsolidateRequest) (MemorySnapshot, error) {
	if err := ctx.Err(); err != nil {
		return MemorySnapshot{}, err
	}
	entries := make(map[string]ConsolidatedEntry)
	if request.Previous != nil {
		for _, entry := range request.Previous.Entries {
			if entry.Status == "" || entry.Status == "active" {
				entry.Status = "active"
				entries[entry.Key] = entry
			}
		}
	}
	inputs := append([]RawMemory(nil), request.Inputs...)
	sort.SliceStable(inputs, func(i, j int) bool {
		if inputs[i].GeneratedAt.Equal(inputs[j].GeneratedAt) {
			return inputs[i].ID < inputs[j].ID
		}
		return inputs[i].GeneratedAt.Before(inputs[j].GeneratedAt)
	})
	for _, raw := range inputs {
		for _, item := range raw.Categories {
			candidate := ConsolidatedEntry{Key: item.Key, Kind: item.Kind, Content: item.Content, SourceMemoryIDs: []string{raw.ID}, Confidence: item.Confidence, FirstSeenAt: raw.GeneratedAt, LastSeenAt: raw.GeneratedAt, Status: "active"}
			current, exists := entries[item.Key]
			if !exists {
				entries[item.Key] = candidate
				continue
			}
			if current.FirstSeenAt.IsZero() || raw.GeneratedAt.Before(current.FirstSeenAt) {
				current.FirstSeenAt = raw.GeneratedAt
			}
			if raw.GeneratedAt.After(current.LastSeenAt) {
				current.LastSeenAt = raw.GeneratedAt
			}
			if !contains(current.SourceMemoryIDs, raw.ID) {
				current.SourceMemoryIDs = append(current.SourceMemoryIDs, raw.ID)
			}
			if item.Content == current.Content {
				if item.Confidence > current.Confidence {
					current.Confidence = item.Confidence
				}
				entries[item.Key] = current
				continue
			}
			if item.Kind == Correction || item.Confidence >= current.Confidence {
				current.Kind, current.Content, current.Confidence = item.Kind, item.Content, item.Confidence
			}
			entries[item.Key] = current
		}
	}
	ordered := make([]ConsolidatedEntry, 0, len(entries))
	for _, entry := range entries {
		sort.Strings(entry.SourceMemoryIDs)
		ordered = append(ordered, entry)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	now := time.Now()
	watermark := ""
	if len(inputs) > 0 {
		watermark = inputs[len(inputs)-1].ID
	}
	return MemorySnapshot{Entries: ordered, Summary: renderSummary(ordered), Detailed: renderDetailed(ordered), InputWatermark: watermark, InputRawMemoryIDs: rawIDs(inputs), CreatedAt: now, PromptVersion: 1}, nil
}

type LLMConsolidator struct {
	Client   llm.LLMClient
	Model    string
	Fallback DeterministicConsolidator
}

func (c LLMConsolidator) Consolidate(ctx context.Context, request ConsolidateRequest) (MemorySnapshot, error) {
	if c.Client == nil {
		return c.Fallback.Consolidate(ctx, request)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		return MemorySnapshot{}, err
	}
	events, errs := c.Client.Stream(&llm.StreamRequest{Context: ctx, SystemPrompt: consolidationPrompt, Messages: []conversation.Message{{Role: conversation.USER, Content: string(payload)}}})
	var output strings.Builder
	for events != nil || errs != nil {
		select {
		case <-ctx.Done():
			return MemorySnapshot{}, ctx.Err()
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return c.Fallback.Consolidate(ctx, request)
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
				return c.Fallback.Consolidate(ctx, request)
			}
		}
	}
	var snapshot MemorySnapshot
	if err := json.Unmarshal([]byte(strings.TrimSpace(output.String())), &snapshot); err != nil {
		return c.Fallback.Consolidate(ctx, request)
	}
	if snapshot.Summary == "" {
		return c.Fallback.Consolidate(ctx, request)
	}
	return snapshot, nil
}

const consolidationPrompt = `Consolidate evidence-backed coding memories. Return JSON MemorySnapshot with entries, summary, detailed, and no instructions or secrets. Prefer explicit corrections and current evidence. Do not call tools.`

func renderSummary(entries []ConsolidatedEntry) string {
	var builder strings.Builder
	for _, entry := range entries {
		if entry.Status == "active" && entry.Confidence >= 0.7 {
			fmt.Fprintf(&builder, "- [%s] %s\n", entry.Kind, entry.Content)
		}
	}
	return strings.TrimSpace(builder.String())
}

func renderDetailed(entries []ConsolidatedEntry) string {
	var builder strings.Builder
	builder.WriteString("# Memory\n\n")
	for _, entry := range entries {
		fmt.Fprintf(&builder, "## %s\n\n- kind: %s\n- confidence: %.2f\n- sources: %s\n\n%s\n\n", entry.Key, entry.Kind, entry.Confidence, strings.Join(entry.SourceMemoryIDs, ", "), entry.Content)
	}
	return strings.TrimSpace(builder.String()) + "\n"
}

func rawIDs(inputs []RawMemory) []string {
	result := make([]string, 0, len(inputs))
	for _, item := range inputs {
		result = append(result, item.ID)
	}
	return result
}
func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
