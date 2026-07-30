package agent

import (
	contextmanager "FFCode/internal/context"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"FFCode/internal/conversation"
	"FFCode/internal/llm"
)

const CheckpointFormatVersion = 1

var ErrCheckpointNotFound = errors.New("run checkpoint not found")

type CheckpointBoundary string

const (
	CheckpointModel       CheckpointBoundary = "model"
	CheckpointTools       CheckpointBoundary = "tools"
	CheckpointRecovery    CheckpointBoundary = "recovery"
	CheckpointInterrupted CheckpointBoundary = "interrupted"
	CheckpointCompleted   CheckpointBoundary = "completed"
)

type RunCheckpoint struct {
	Version              int                         `json:"version"`
	Generation           uint64                      `json:"generation"`
	SessionID            string                      `json:"session_id"`
	Boundary             CheckpointBoundary          `json:"boundary"`
	Completed            bool                        `json:"completed"`
	Workspace            string                      `json:"workspace,omitempty"`
	WorkspaceFingerprint string                      `json:"workspace_fingerprint,omitempty"`
	History              []conversation.Message      `json:"history,omitempty"`
	PendingToolUses      []conversation.ToolUseBlock `json:"pending_tool_uses,omitempty"`
	CompletedToolUseIDs  []string                    `json:"completed_tool_use_ids,omitempty"`
	UpdatedAt            time.Time                   `json:"updated_at"`
}

// CheckpointStore persists committed agent-loop boundaries. Implementations
// must make Save atomic for a single session.
type CheckpointStore interface {
	Load(ctx context.Context, sessionID string) (RunCheckpoint, error)
	Save(ctx context.Context, checkpoint RunCheckpoint) error
}

func (a *Agent) recoverCheckpoint(ctx context.Context, conversationContext *contextmanager.ConversationContext, fingerprinter ProgressFingerprinter) (string, error) {
	if a.CheckpointStore == nil {
		return "", nil
	}
	checkpoint, err := a.CheckpointStore.Load(ctx, conversationContext.SessionID)
	if errors.Is(err, ErrCheckpointNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("load run checkpoint: %w", err)
	}
	if checkpoint.Completed {
		return "", nil
	}
	if checkpoint.Version != CheckpointFormatVersion || checkpoint.SessionID != conversationContext.SessionID {
		return "", errors.New("run checkpoint is incompatible with this session")
	}

	currentHistory := cloneHistory(conversationContext.History)
	common := commonHistoryPrefix(checkpoint.History, currentHistory)
	recovered := cloneHistory(checkpoint.History)
	completed := make(map[string]struct{}, len(checkpoint.CompletedToolUseIDs))
	for _, toolUseID := range checkpoint.CompletedToolUseIDs {
		completed[toolUseID] = struct{}{}
	}
	var reconciled []conversation.ToolResultBlock
	for _, pending := range checkpoint.PendingToolUses {
		if _, ok := completed[pending.ToolUseID]; ok {
			continue
		}
		reconciled = append(reconciled, conversation.ToolResultBlock{
			ToolUseID: pending.ToolUseID,
			Content:   "tool execution was interrupted and was not replayed; inspect the current workspace before deciding whether to retry",
			IsError:   true,
		})
	}
	if len(reconciled) > 0 {
		recovered = append(recovered, conversation.Message{Role: conversation.TOOL, ToolResults: reconciled})
	}
	recovered = append(recovered, cloneHistory(currentHistory[common:])...)
	conversationContext.History = recovered

	currentFingerprint := workspaceFingerprint(ctx, fingerprinter, conversationContext.Workspace)
	guidance := "A previous run was interrupted. Completed tool calls were preserved and pending calls were not replayed; inspect current state before making further changes."
	if checkpoint.WorkspaceFingerprint != "" && checkpoint.WorkspaceFingerprint != currentFingerprint {
		guidance += " The workspace changed since the checkpoint, so re-read relevant files before writing."
	}
	checkpoint.Boundary = CheckpointRecovery
	checkpoint.History = cloneHistory(conversationContext.History)
	checkpoint.PendingToolUses = nil
	checkpoint.WorkspaceFingerprint = currentFingerprint
	checkpoint.UpdatedAt = time.Now()
	if err := a.CheckpointStore.Save(ctx, checkpoint); err != nil {
		return "", fmt.Errorf("save recovered checkpoint: %w", err)
	}
	return guidance, nil
}

func (a *Agent) saveCheckpoint(ctx context.Context, conversationContext *contextmanager.ConversationContext, fingerprinter ProgressFingerprinter, boundary CheckpointBoundary, pending []llm.ToolCallComplete, completedIDs []string, completed bool) error {
	if a.CheckpointStore == nil {
		return nil
	}
	fingerprint := workspaceFingerprint(ctx, fingerprinter, conversationContext.Workspace)
	return a.saveCheckpointWithFingerprint(ctx, conversationContext, boundary, pending, completedIDs, completed, fingerprint)
}

func (a *Agent) saveCheckpointWithFingerprint(ctx context.Context, conversationContext *contextmanager.ConversationContext, boundary CheckpointBoundary, pending []llm.ToolCallComplete, completedIDs []string, completed bool, fingerprint string) error {
	if a.CheckpointStore == nil {
		return nil
	}
	pendingUses := make([]conversation.ToolUseBlock, 0, len(pending))
	for _, call := range pending {
		pendingUses = append(pendingUses, conversation.ToolUseBlock{ToolUseID: call.ToolID, ToolName: call.ToolName, Arguments: cloneArguments(call.Arguments)})
	}
	checkpoint := RunCheckpoint{
		Version:              CheckpointFormatVersion,
		SessionID:            conversationContext.SessionID,
		Boundary:             boundary,
		Completed:            completed,
		Workspace:            conversationContext.Workspace,
		WorkspaceFingerprint: fingerprint,
		History:              cloneHistory(conversationContext.History),
		PendingToolUses:      pendingUses,
		CompletedToolUseIDs:  append([]string(nil), completedIDs...),
		UpdatedAt:            time.Now(),
	}
	if err := a.CheckpointStore.Save(ctx, checkpoint); err != nil {
		return fmt.Errorf("save run checkpoint: %w", err)
	}
	return nil
}

func (a *Agent) saveInterruptedCheckpoint(conversationContext *contextmanager.ConversationContext, fingerprinter ProgressFingerprinter) {
	if a.CheckpointStore == nil || conversationContext == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = a.saveCheckpoint(ctx, conversationContext, fingerprinter, CheckpointInterrupted, nil, nil, false)
}

func commonHistoryPrefix(left, right []conversation.Message) int {
	limit := min(len(left), len(right))
	for index := range limit {
		if !reflect.DeepEqual(left[index], right[index]) {
			return index
		}
	}
	return limit
}

func cloneHistory(history []conversation.Message) []conversation.Message {
	cloned := make([]conversation.Message, len(history))
	for index, message := range history {
		cloned[index] = message
		cloned[index].ThinkingBlocks = append([]conversation.ThinkingBlock(nil), message.ThinkingBlocks...)
		cloned[index].ToolResults = append([]conversation.ToolResultBlock(nil), message.ToolResults...)
		cloned[index].ToolUses = make([]conversation.ToolUseBlock, len(message.ToolUses))
		for toolIndex, toolUse := range message.ToolUses {
			cloned[index].ToolUses[toolIndex] = toolUse
			cloned[index].ToolUses[toolIndex].Arguments = cloneArguments(toolUse.Arguments)
		}
	}
	return cloned
}

func cloneArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	cloned := make(map[string]any, len(arguments))
	for key, value := range arguments {
		cloned[key] = value
	}
	return cloned
}

func completedToolIDs(calls []llm.ToolCallComplete) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		if strings.TrimSpace(call.ToolID) != "" {
			ids = append(ids, call.ToolID)
		}
	}
	return ids
}
