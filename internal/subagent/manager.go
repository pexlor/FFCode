package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"FFCode/internal/agent"
	contextmanager "FFCode/internal/context"
	"FFCode/internal/conversation"
	"FFCode/internal/hook"
	"FFCode/internal/llm"
)

const maxEvidenceBytes = 4096

var (
	ErrTaskRequired      = errors.New("subagent task is required")
	ErrWorkspaceRequired = errors.New("subagent workspace is required")
	ErrSubagentLimit     = errors.New("subagent call limit reached")
)

type Config struct {
	MaxConcurrent int
	MaxPerRun     int
	DefaultBudget agent.RunBudget
}

type Manager struct {
	client llm.LLMClient
	hooks  *hook.Dispatcher
	config Config
	slots  chan struct{}
}

func NewManager(client llm.LLMClient, hooks *hook.Dispatcher, config Config) (*Manager, error) {
	if client == nil {
		return nil, errors.New("subagent LLM client cannot be nil")
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 4
	}
	if config.MaxPerRun <= 0 {
		config.MaxPerRun = 8
	}
	defaults := config.DefaultBudget
	if defaults.MaxDuration <= 0 {
		defaults.MaxDuration = 2 * time.Minute
	}
	if defaults.MaxInputTokens <= 0 {
		defaults.MaxInputTokens = 100_000
	}
	if defaults.MaxOutputTokens <= 0 {
		defaults.MaxOutputTokens = 4_000
	}
	if defaults.MaxToolCalls <= 0 {
		defaults.MaxToolCalls = 30
	}
	if defaults.MaxProviderRetries <= 0 {
		defaults.MaxProviderRetries = 1
	}
	config.DefaultBudget = defaults
	return &Manager{client: client, hooks: hooks, config: config, slots: make(chan struct{}, config.MaxConcurrent)}, nil
}

func (m *Manager) Delegate(ctx context.Context, request Request) Result {
	if strings.TrimSpace(request.Task) == "" {
		return failedResult(StatusRejected, ErrTaskRequired)
	}
	if strings.TrimSpace(request.Workspace) == "" {
		return failedResult(StatusRejected, ErrWorkspaceRequired)
	}
	if !agent.ClaimSubagentCall(ctx, m.config.MaxPerRun) {
		return failedResult(StatusRejected, ErrSubagentLimit)
	}
	requested := mergeBudget(request.Budget, m.config.DefaultBudget)
	reservation, err := agent.ReserveChildBudget(ctx, requested)
	if err != nil {
		return failedResult(StatusBudgetExceeded, err)
	}
	committed := false
	defer func() {
		if !committed {
			reservation.Release()
		}
	}()

	select {
	case m.slots <- struct{}{}:
		defer func() { <-m.slots }()
	case <-ctx.Done():
		return failedResult(statusFromContext(ctx), ctx.Err())
	}

	result := m.run(ctx, request, reservation.Budget)
	reservation.Commit(result.Usage.llmUsage(), result.toolCalls)
	committed = true
	return result.Result
}

type runResult struct {
	Result
	toolCalls int
}

func (m *Manager) run(ctx context.Context, request Request, budget agent.RunBudget) runResult {
	subagentID, err := newID("subagent")
	if err != nil {
		return runResult{Result: failedResult(StatusFailed, err)}
	}
	sessionID, err := newID("session")
	if err != nil {
		return runResult{Result: failedResult(StatusFailed, err)}
	}
	tools, err := newReadOnlyTools(request.Workspace)
	if err != nil {
		return runResult{Result: failedResult(StatusFailed, err)}
	}
	child, err := agent.NewAgent(ctx, m.client, tools)
	if err != nil {
		return runResult{Result: failedResult(StatusFailed, err)}
	}
	conversationContext := &contextmanager.ConversationContext{
		SessionID:    sessionID,
		Workspace:    request.Workspace,
		SystemPrompt: childSystemPrompt(),
		History:      []conversation.Message{{Role: conversation.USER, Content: childRequest(request)}},
	}
	result := runResult{Result: Result{SubagentID: subagentID, SessionID: sessionID, Status: StatusFailed}}
	agent.EmitAgentEvent(ctx, agent.SubagentStartEvent{
		SubagentID: subagentID, ParentSessionID: request.ParentSessionID, SessionID: sessionID, Task: request.Task,
	})
	hookInput := hook.Input{
		SessionID: request.ParentSessionID,
		Workspace: request.Workspace,
		Prompt:    request.Task,
		Metadata:  map[string]any{"subagent_id": subagentID, "subagent_session_id": sessionID},
	}
	lifecycle := &agent.Agent{Hooks: m.hooks}
	runErr := lifecycle.RunSubagent(ctx, hookInput, func(runCtx context.Context) error {
		return collectChild(runCtx, child.RunContextWithBudget(runCtx, conversationContext, budget), request.Workspace, &result)
	})
	if runErr != nil && result.Err == nil {
		result.Err = runErr
		result.Error = runErr.Error()
		result.Status = statusFromContext(ctx)
	}
	agent.EmitAgentEvent(ctx, agent.SubagentStopEvent{
		SubagentID: subagentID, SessionID: sessionID, Status: string(result.Status), Usage: result.Usage.llmUsage(), Err: result.Err,
	})
	return result
}

func collectChild(ctx context.Context, events <-chan agent.AgentEvent, workspace string, result *runResult) error {
	toolSources := make(map[string]string)
	files := make(map[string]struct{})
	var text strings.Builder
	for event := range events {
		agent.EmitAgentEvent(ctx, agent.SubagentEvent{SubagentID: result.SubagentID, Event: event})
		switch item := event.(type) {
		case agent.TextEvent:
			text.WriteString(item.Text)
		case agent.ToolCallCompleteEvent:
			source := sourceFromArguments(item.ToolName, item.Arguments, workspace)
			toolSources[item.ToolUseID] = source
			if source != "" {
				files[source] = struct{}{}
			}
		case agent.ToolExecutionStartEvent:
			result.toolCalls++
		case agent.ToolResultEvent:
			if !item.IsError {
				result.Evidence = append(result.Evidence, Evidence{
					Kind: "tool_result", Source: toolSources[item.ToolUseID], Content: boundText(item.Content, maxEvidenceBytes), Important: true,
				})
			}
		case agent.TurnEndEvent:
			result.Usage = usageFromLLM(item.Usage)
			result.StopReason = item.StopReason
			result.Status = statusFromTurn(ctx, item)
			result.Err = item.Err
			if item.Err != nil {
				result.Error = item.Err.Error()
			}
		}
	}
	result.Summary = strings.TrimSpace(text.String())
	for path := range files {
		result.FilesRead = append(result.FilesRead, path)
	}
	sort.Strings(result.FilesRead)
	if result.Err != nil {
		return result.Err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func mergeBudget(requested, defaults agent.RunBudget) agent.RunBudget {
	if requested.MaxDuration == 0 {
		requested.MaxDuration = defaults.MaxDuration
	}
	if requested.MaxInputTokens == 0 {
		requested.MaxInputTokens = defaults.MaxInputTokens
	}
	if requested.MaxOutputTokens == 0 {
		requested.MaxOutputTokens = defaults.MaxOutputTokens
	}
	if requested.MaxToolCalls == 0 {
		requested.MaxToolCalls = defaults.MaxToolCalls
	}
	if requested.MaxProviderRetries == 0 {
		requested.MaxProviderRetries = defaults.MaxProviderRetries
	}
	return requested
}

func statusFromTurn(parent context.Context, event agent.TurnEndEvent) Status {
	switch event.StopReason {
	case agent.StopBudgetExceeded:
		return StatusBudgetExceeded
	case agent.StopDeadlineExceeded:
		if parent == nil || parent.Err() == nil {
			return StatusBudgetExceeded
		}
		return StatusCanceled
	case agent.StopCancelled:
		return StatusCanceled
	}
	if event.Status == agent.TurnCompleted {
		return StatusCompleted
	}
	return StatusFailed
}

func statusFromContext(ctx context.Context) Status {
	if ctx != nil && ctx.Err() != nil {
		return StatusCanceled
	}
	return StatusFailed
}

func failedResult(status Status, err error) Result {
	result := Result{Status: status, Err: err}
	if err != nil {
		result.Error = err.Error()
	}
	return result
}

func sourceFromArguments(toolName string, arguments map[string]any, workspace string) string {
	switch strings.ToLower(toolName) {
	case "readfile":
		value, _ := arguments["file_path"].(string)
		return value
	case "grep", "glob":
		value, _ := arguments["path"].(string)
		if strings.TrimSpace(value) == "" {
			return workspace
		}
		return value
	default:
		return ""
	}
}

func childSystemPrompt() string {
	return "You are a read-only subagent. Inspect the workspace only with the provided tools. Do not modify files or delegate tasks. Return a concise conclusion supported by exact file paths and line references."
}

func childRequest(request Request) string {
	if strings.TrimSpace(request.AdditionalContext) == "" {
		return request.Task
	}
	return fmt.Sprintf("Task:\n%s\n\nAdditional context:\n%s", request.Task, request.AdditionalContext)
}

func boundText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[truncated]"
}

func newID(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate %s id: %w", prefix, err)
	}
	return prefix + "-" + hex.EncodeToString(value), nil
}
