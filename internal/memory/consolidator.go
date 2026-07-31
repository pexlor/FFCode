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

type DeterministicConsolidator struct {
	SummaryTokenLimit int
}

const defaultMemorySummaryTokenLimit = 8000

func (c DeterministicConsolidator) Consolidate(ctx context.Context, request ConsolidateRequest) (MemorySnapshot, error) {
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
			candidate := ConsolidatedEntry{Key: item.Key, Kind: item.Kind, Scope: item.Scope, Content: item.Content, SourceMemoryIDs: []string{raw.ID}, Confidence: item.Confidence, FirstSeenAt: raw.GeneratedAt, LastSeenAt: raw.GeneratedAt, Status: "active"}
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
				current.Kind, current.Scope, current.Content, current.Confidence = item.Kind, item.Scope, item.Content, item.Confidence
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
	limit := c.SummaryTokenLimit
	if limit <= 0 {
		limit = defaultMemorySummaryTokenLimit
	}
	return MemorySnapshot{Entries: ordered, Summary: renderSummary(ordered, limit), Detailed: renderDetailed(ordered), InputWatermark: watermark, InputRawMemoryIDs: rawIDs(inputs), CreatedAt: now, PromptVersion: 1}, nil
}

type LLMConsolidator struct {
	Client            llm.LLMClient
	Model             string
	SummaryTokenLimit int
	Fallback          DeterministicConsolidator
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
	if err := validateConsolidatedEntries(snapshot.Entries, request); err != nil {
		return c.Fallback.Consolidate(ctx, request)
	}
	limit := c.SummaryTokenLimit
	if limit <= 0 {
		limit = defaultMemorySummaryTokenLimit
	}
	snapshot.Summary = renderSummary(snapshot.Entries, limit)
	snapshot.Detailed = renderDetailed(snapshot.Entries)
	snapshot.InputRawMemoryIDs = rawIDs(request.Inputs)
	if len(request.Inputs) > 0 {
		snapshot.InputWatermark = request.Inputs[len(request.Inputs)-1].ID
	}
	snapshot.CreatedAt = time.Now()
	snapshot.Model = c.Model
	snapshot.PromptVersion = 1
	return snapshot, nil
}

const consolidationPrompt = `Consolidate evidence-backed coding memories. Return one JSON object with entries.
Preserve every active previous key and every input key. Each entry must include key, kind, scope,
content, source_memory_ids, confidence, first_seen_at, last_seen_at, and one of these statuses:
active, superseded, expired, pending_conflict, rejected. Source IDs must come from the input.
Prefer explicit user corrections and direct current evidence. Do not add instructions or secrets.
Do not call tools. Summary and detailed text are rendered by the caller.`

func renderSummary(entries []ConsolidatedEntry, limit int) string {
	if limit <= 0 {
		limit = defaultMemorySummaryTokenLimit
	}
	var builder strings.Builder
	for _, entry := range entries {
		if entry.Status == "active" && entry.Scope != "session" && entry.Confidence >= 0.7 {
			line := fmt.Sprintf("- [%s] %s\n", entry.Kind, entry.Content)
			remaining := limit - len([]rune(builder.String()))
			if remaining <= 0 {
				break
			}
			runes := []rune(line)
			if len(runes) > remaining {
				if remaining > 3 {
					builder.WriteString(string(runes[:remaining-3]))
					builder.WriteString("...")
				}
				break
			}
			builder.WriteString(line)
		}
	}
	return strings.TrimSpace(builder.String())
}

func validateConsolidatedEntries(entries []ConsolidatedEntry, request ConsolidateRequest) error {
	allowedSources := make(map[string]bool)
	requiredKeys := make(map[string]bool)
	if request.Previous != nil {
		for _, entry := range request.Previous.Entries {
			if entry.Status == "" || entry.Status == "active" {
				requiredKeys[entry.Key] = true
			}
			for _, source := range entry.SourceMemoryIDs {
				allowedSources[source] = true
			}
		}
	}
	for _, raw := range request.Inputs {
		allowedSources[raw.ID] = true
		for _, item := range raw.Categories {
			requiredKeys[item.Key] = true
		}
	}
	seen := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Key) == "" || seen[entry.Key] || !validKind(entry.Kind) || !validScope(entry.Scope) || strings.TrimSpace(entry.Content) == "" || secretPattern.MatchString(entry.Content) || entry.Confidence < 0 || entry.Confidence > 1 {
			return ErrInvalidMemory
		}
		seen[entry.Key] = true
		delete(requiredKeys, entry.Key)
		switch entry.Status {
		case "active", "superseded", "expired", "pending_conflict", "rejected":
		default:
			return ErrInvalidMemory
		}
		if len(entry.SourceMemoryIDs) == 0 {
			return ErrInvalidMemory
		}
		for _, source := range entry.SourceMemoryIDs {
			if !allowedSources[source] {
				return ErrInvalidMemory
			}
		}
	}
	if len(requiredKeys) != 0 {
		return ErrInvalidMemory
	}
	return nil
}

func renderDetailed(entries []ConsolidatedEntry) string {
	var builder strings.Builder
	builder.WriteString("# Memory\n\n")
	for _, entry := range entries {
		fmt.Fprintf(&builder, "## %s\n\n- kind: %s\n- scope: %s\n- status: %s\n- confidence: %.2f\n- sources: %s\n\n%s\n\n", entry.Key, entry.Kind, entry.Scope, entry.Status, entry.Confidence, strings.Join(entry.SourceMemoryIDs, ", "), entry.Content)
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
