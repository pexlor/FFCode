package agent

import (
	contextmanager "MyCode/internal/context"
	"MyCode/internal/conversation"
	"MyCode/internal/llm"
	"MyCode/internal/skill"
	"MyCode/internal/tool"
	"context"
	"errors"
	"fmt"
	"strings"
)

const DefaultMaxIterations = 800

const malformedToolInputRetries = 1

type Agent struct {
	ctx            context.Context
	client         llm.LLMClient
	toolManager    *tool.ToolsManager
	contextManager *contextmanager.ContextManager
	skillManager   *skill.Manager
	MaxIterations  int
	RunBudget      RunBudget
}

func NewAgent(ctx context.Context, client llm.LLMClient, toolManager *tool.ToolsManager) (*Agent, error) {
	if ctx == nil {
		return nil, errors.New("agent context cannot be nil")
	}
	if client == nil {
		return nil, errors.New("llm client cannot be nil")
	}
	if toolManager == nil {
		toolManager = tool.NewToolsManager()
	}
	return &Agent{
		ctx:           ctx,
		client:        client,
		toolManager:   toolManager,
		MaxIterations: DefaultMaxIterations,
		RunBudget:     DefaultRunBudget(),
	}, nil
}

func (a *Agent) SetContextManager(manager *contextmanager.ContextManager) {
	a.contextManager = manager
}

// SetSkillManager enables metadata-only Skill catalog injection and active
// Skill SOP/tool filtering for subsequent model requests.
func (a *Agent) SetSkillManager(manager *skill.Manager) { a.skillManager = manager }

// SetThinkingEnabled updates the mode used by subsequent model requests.
func (a *Agent) SetThinkingEnabled(enabled bool) error {
	controller, ok := a.client.(llm.ThinkingModeController)
	if !ok {
		return errors.New("the configured model protocol does not support toggling thinking mode")
	}
	controller.SetThinkingEnabled(enabled)
	return nil
}

func (a *Agent) ThinkingEnabled() (bool, error) {
	controller, ok := a.client.(llm.ThinkingModeController)
	if !ok {
		return false, errors.New("the configured model protocol does not support toggling thinking mode")
	}
	return controller.ThinkingEnabled(), nil
}

// Run executes the agent loop and emits events for upper-layer UI.
func (a *Agent) Run(session *conversation.Session) <-chan AgentEvent {
	return a.RunContext(agentContext(a), session)
}

// RunContext executes one agent turn with a caller-controlled context. This
// lets an interactive UI interrupt an in-flight model request or tool call.
func (a *Agent) RunContext(ctx context.Context, session *conversation.Session) <-chan AgentEvent {
	var budget RunBudget
	if a != nil {
		budget = a.RunBudget
	}
	return a.RunContextWithBudget(ctx, session, budget)
}

// RunContextWithBudget executes one turn with caller-supplied resource limits.
func (a *Agent) RunContextWithBudget(ctx context.Context, session *conversation.Session, budget RunBudget) <-chan AgentEvent {
	agentEventCh := make(chan AgentEvent, 32)
	if ctx == nil {
		ctx = agentContext(a)
	}

	go func() {
		defer close(agentEventCh)
		budgetState, err := newRunBudgetState(budget)
		if err != nil {
			sendTurnEndEvent(agentEventCh, turnEndFromError(err, llm.UsageInfo{}))
			return
		}
		ctx, cancel := budgetState.context(ctx)
		defer cancel()

		if err := a.validate(session); err != nil {
			sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
			return
		}
		if err := ctx.Err(); err != nil {
			sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
			return
		}

		for iteration := 0; iteration < a.MaxIterations; iteration++ {
			toolSchemas := a.toolManager.BuildAllSchemas()
			systemPrompt := session.SystemPrompt
			if a.skillManager != nil {
				systemPrompt = appendSkillPrompt(systemPrompt, a.skillManager.CatalogPrompt(), a.skillManager.Instructions())
				toolSchemas = filterSkillTools(toolSchemas, a.skillManager.AllowedTools())
			}
			history := session.History
			if a.contextManager != nil {
				// 每次模型请求（包括同一 Turn 中的工具循环）都重新构建 ContextView。
				// Build 内部通过同步游标避免重复写 transcript，并通过摘要检查点避免重复压缩。
				view, err := a.contextManager.Build(ctx, contextmanager.BuildInput{
					Session: session, CurrentRequest: latestUserRequest(session.History),
					AvailableTools: toolSchemas,
				})
				if err != nil {
					sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
					return
				}
				// 从这里开始，LLM 只接触经过预算治理的视图，不再直接接触完整 History。
				systemPrompt = view.SystemPrompt
				history = view.Messages
				toolSchemas = view.Tools
			}
			var assistantText, stopReason string
			var thinkingBlocks []conversation.ThinkingBlock
			var toolCalls []llm.ToolCallComplete
			var usage llm.UsageInfo
			for attempt := 0; ; attempt++ {
				sendAgentEvent(ctx, agentEventCh, ThinkingStartEvent{})
				events, errs := a.client.Stream(&llm.StreamRequest{
					Context:      ctx,
					SystemPrompt: systemPrompt,
					Messages:     history,
					Tools:        toolSchemas,
				})

				var err error
				assistantText, thinkingBlocks, toolCalls, stopReason, usage, err = a.handleStream(ctx, events, errs, agentEventCh)
				if budgetErr := budgetState.recordUsage(usage); budgetErr != nil {
					sendTurnEndEvent(agentEventCh, turnEndFromError(budgetErr, budgetState.usage))
					return
				}
				if err == nil {
					break
				}
				if errors.Is(err, llm.ErrMalformedToolInput) && attempt < malformedToolInputRetries {
					continue
				}
				sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
				return
			}

			if len(toolCalls) == 0 {
				if assistantText != "" || len(thinkingBlocks) > 0 {
					session.History = append(session.History, conversation.Message{
						Role:           conversation.ASSISTANT,
						Content:        assistantText,
						ThinkingBlocks: thinkingBlocks,
					})
				}
				if a.contextManager != nil {
					if err := a.contextManager.SyncSession(ctx, session); err != nil {
						sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
						return
					}
				}
				sendTurnEndEvent(agentEventCh, turnEndFromStopReason(stopReason, budgetState.usage))
				return
			}
			if err := budgetState.reserveToolCalls(len(toolCalls)); err != nil {
				sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
				return
			}

			session.History = append(session.History, conversation.Message{
				Role:           conversation.ASSISTANT,
				Content:        assistantText,
				ThinkingBlocks: thinkingBlocks,
				ToolUses:       toToolUseBlocks(toolCalls),
			})

			toolResults := a.executeTools(ctx, toolCalls, agentEventCh)
			session.AddToolResult(toolResults)
		}

		err = fmt.Errorf("agent loop exceeded max iterations %d", a.MaxIterations)
		sendTurnEndEvent(agentEventCh, turnEndFromError(err, budgetState.usage))
	}()

	return agentEventCh
}

func appendSkillPrompt(base string, sections ...string) string {
	for _, section := range sections {
		section = strings.TrimSpace(section)
		if section == "" {
			continue
		}
		if strings.TrimSpace(base) != "" {
			base += "\n\n"
		}
		base += section
	}
	return base
}

func filterSkillTools(schemas []*tool.ToolSchema, allowed map[string]struct{}) []*tool.ToolSchema {
	if allowed == nil {
		return schemas
	}
	filtered := make([]*tool.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if schema == nil {
			continue
		}
		if schema.Name == skill.LoadToolName {
			filtered = append(filtered, schema)
			continue
		}
		if _, ok := allowed[strings.ToLower(schema.Name)]; ok {
			filtered = append(filtered, schema)
		}
	}
	return filtered
}

func turnEndFromStopReason(providerReason string, usage llm.UsageInfo) TurnEndEvent {
	result := TurnEndEvent{ProviderReason: providerReason, Usage: usage}
	switch strings.ToLower(strings.TrimSpace(providerReason)) {
	case "end_turn", "stop", "stop_sequence":
		result.Status = TurnCompleted
		result.StopReason = StopEndTurn
	case "max_tokens", "length":
		result.Status = TurnIncomplete
		result.StopReason = StopMaxTokens
	default:
		result.Status = TurnIncomplete
		result.StopReason = StopAgentError
	}
	return result
}

func turnEndFromError(err error, usage llm.UsageInfo) TurnEndEvent {
	result := TurnEndEvent{Status: TurnFailed, StopReason: StopAgentError, Usage: usage, Err: err}
	switch {
	case errors.As(err, new(*budgetExceededError)):
		result.Status = TurnIncomplete
		result.StopReason = StopBudgetExceeded
	case errors.Is(err, context.Canceled):
		result.Status = TurnCancelled
		result.StopReason = StopCancelled
	case errors.Is(err, context.DeadlineExceeded):
		result.StopReason = StopDeadlineExceeded
	}
	return result
}

// Terminal events must survive cancellation of the turn context. The channel
// is buffered and the caller drains it until close, so one final send is safe.
func sendTurnEndEvent(ch chan<- AgentEvent, event TurnEndEvent) {
	ch <- event
}

func latestUserRequest(history []conversation.Message) string {
	// 工具循环会在用户消息后追加多条 assistant/tool 消息，因此必须逆序寻找最近用户请求。
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == conversation.USER {
			return history[index].Content
		}
	}
	return ""
}

func (a *Agent) run(session *conversation.Session) <-chan AgentEvent {
	return a.Run(session)
}

func (a *Agent) validate(session *conversation.Session) error {
	if a == nil {
		return errors.New("agent cannot be nil")
	}
	if a.ctx == nil {
		return errors.New("agent context cannot be nil")
	}
	if a.client == nil {
		return errors.New("llm client cannot be nil")
	}
	if a.toolManager == nil {
		return errors.New("tool manager cannot be nil")
	}
	if session == nil {
		return errors.New("session cannot be nil")
	}
	if a.MaxIterations <= 0 {
		return errors.New("max iterations must be greater than zero")
	}
	return nil
}

func (a *Agent) handleStream(ctx context.Context, events <-chan llm.StreamEvent, errs <-chan error, out chan<- AgentEvent) (string, []conversation.ThinkingBlock, []llm.ToolCallComplete, string, llm.UsageInfo, error) {
	var assistantText strings.Builder
	var thinkingBlocks []conversation.ThinkingBlock
	var toolCalls []llm.ToolCallComplete
	stopReason := ""
	var usage llm.UsageInfo

	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			switch ev := event.(type) {
			case llm.TextStream:
				assistantText.WriteString(ev.Text)
				sendAgentEvent(ctx, out, TextEvent{Text: ev.Text})
			case llm.ThinkingStream:
				sendAgentEvent(ctx, out, ThinkingEvent{Text: ev.Text})
			case llm.ThinkingComplete:
				thinkingBlocks = append(thinkingBlocks, conversation.ThinkingBlock{Thinking: ev.Thinking, Signature: ev.Signature})
			case llm.ToolCallStart:
				sendAgentEvent(ctx, out, ToolCallStartEvent{ToolUseID: ev.ToolID, ToolName: ev.ToolName})
			case llm.ToolCallStream:
				sendAgentEvent(ctx, out, ToolCallDeltaEvent{ToolUseID: ev.ToolID, Text: ev.Text})
			case llm.ToolCallComplete:
				toolCalls = append(toolCalls, ev)
				sendAgentEvent(ctx, out, ToolCallCompleteEvent{ToolUseID: ev.ToolID, ToolName: ev.ToolName, Arguments: ev.Arguments})
			case llm.StreamEnd:
				stopReason = ev.StopReason
				usage = ev.Usage
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				return assistantText.String(), thinkingBlocks, toolCalls, stopReason, usage, err
			}
		case <-ctx.Done():
			return assistantText.String(), thinkingBlocks, toolCalls, stopReason, usage, ctx.Err()
		}
	}

	return assistantText.String(), thinkingBlocks, toolCalls, stopReason, usage, nil
}

func addUsage(u, other llm.UsageInfo) llm.UsageInfo {
	return llm.UsageInfo{
		InputTokens:         u.InputTokens + other.InputTokens,
		OutputTokens:        u.OutputTokens + other.OutputTokens,
		TotalTokens:         u.TotalTokens + other.TotalTokens,
		CacheReadTokens:     u.CacheReadTokens + other.CacheReadTokens,
		CacheCreationTokens: u.CacheCreationTokens + other.CacheCreationTokens,
	}
}

func (a *Agent) executeTool(ctx context.Context, call llm.ToolCallComplete) tool.ToolResult {
	if call.ToolID == "" || call.ToolName == "" {
		return tool.ToolResult{Output: "tool call is missing id or name", IsError: true}
	}

	return a.toolManager.Execute(ctx, call.ToolName, call.Arguments)
}

type completedToolCall struct {
	index  int
	call   llm.ToolCallComplete
	result tool.ToolResult
}

// executeTools starts every tool call in an iteration concurrently. Results are
// returned in call order because tool-result protocols associate the response
// sequence with the corresponding assistant tool-use sequence.
func (a *Agent) executeTools(ctx context.Context, calls []llm.ToolCallComplete, events chan<- AgentEvent) []conversation.ToolResultBlock {
	completed := make(chan completedToolCall, len(calls))
	for index, call := range calls {
		sendAgentEvent(ctx, events, ToolExecutionStartEvent{
			ToolUseID: call.ToolID,
			ToolName:  call.ToolName,
		})
		go func(index int, call llm.ToolCallComplete) {
			completed <- completedToolCall{index: index, call: call, result: a.executeTool(ctx, call)}
		}(index, call)
	}

	results := make([]conversation.ToolResultBlock, len(calls))
	for index, call := range calls {
		results[index] = conversation.ToolResultBlock{
			ToolUseID: call.ToolID,
			Content:   "tool execution canceled",
			IsError:   true,
		}
	}
	for range calls {
		var outcome completedToolCall
		select {
		case outcome = <-completed:
		case <-ctx.Done():
			return results
		}
		results[outcome.index] = conversation.ToolResultBlock{
			ToolUseID: outcome.call.ToolID,
			Content:   outcome.result.Output,
			IsError:   outcome.result.IsError,
		}
		sendAgentEvent(ctx, events, ToolResultEvent{
			ToolUseID: outcome.call.ToolID,
			ToolName:  outcome.call.ToolName,
			Content:   outcome.result.Output,
			IsError:   outcome.result.IsError,
		})
	}
	return results
}

func toToolUseBlocks(toolCalls []llm.ToolCallComplete) []conversation.ToolUseBlock {
	toolUses := make([]conversation.ToolUseBlock, 0, len(toolCalls))
	for _, call := range toolCalls {
		toolUses = append(toolUses, conversation.ToolUseBlock{
			ToolUseID: call.ToolID,
			ToolName:  call.ToolName,
			Arguments: call.Arguments,
		})
	}
	return toolUses
}

func sendAgentEvent(ctx context.Context, ch chan<- AgentEvent, event AgentEvent) bool {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	select {
	case ch <- event:
		return true
	case <-ctx.Done():
		return false
	}
}

func agentContext(a *Agent) context.Context {
	if a == nil || a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}
