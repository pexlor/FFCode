package agent

import (
	contextmanager "MyCode/internal/context"
	"MyCode/internal/conversation"
	"MyCode/internal/hook"
	"MyCode/internal/llm"
	"MyCode/internal/skill"
	"MyCode/internal/tool"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const DefaultMaxIterations = 800

type Agent struct {
	ctx                    context.Context
	client                 llm.LLMClient
	toolManager            *tool.ToolsManager
	contextManager         *contextmanager.ContextManager
	skillManager           *skill.Manager
	MaxIterations          int
	RunBudget              RunBudget
	ProviderRetryPolicy    llm.RetryPolicy
	ProgressPolicy         ProgressPolicy
	ProgressFingerprinter  ProgressFingerprinter
	ChangeDetector         ChangeDetector
	VerificationClassifier VerificationClassifier
	CheckpointStore        CheckpointStore
	Hooks                  *hook.Dispatcher
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
		ctx:                    ctx,
		client:                 client,
		toolManager:            toolManager,
		MaxIterations:          DefaultMaxIterations,
		RunBudget:              DefaultRunBudget(),
		ProviderRetryPolicy:    llm.DefaultRetryPolicy(),
		ProgressPolicy:         DefaultProgressPolicy(),
		ProgressFingerprinter:  gitProgressFingerprinter{},
		ChangeDetector:         newGitChangeDetector(defaultDiffLimit),
		VerificationClassifier: defaultVerificationClassifier{},
	}, nil
}

func (a *Agent) SetContextManager(manager *contextmanager.ContextManager) {
	a.contextManager = manager
	if manager != nil && a.Hooks != nil {
		manager.SetHookDispatcher(a.Hooks)
	}
}

// SetHookDispatcher installs the shared lifecycle dispatcher in the agent and
// its lower-level tool/context execution paths.
func (a *Agent) SetHookDispatcher(dispatcher *hook.Dispatcher) {
	if a == nil {
		return
	}
	a.Hooks = dispatcher
	if a.toolManager != nil {
		a.toolManager.SetHookDispatcher(dispatcher)
	}
	if a.contextManager != nil {
		a.contextManager.SetHookDispatcher(dispatcher)
	}
}

func (a *Agent) SetHookManager(dispatcher *hook.Dispatcher) { a.SetHookDispatcher(dispatcher) }
func (a *Agent) SetHooks(dispatcher *hook.Dispatcher)       { a.SetHookDispatcher(dispatcher) }

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

func (a *Agent) SetThinkingEffort(value string) error {
	effort, err := llm.ParseThinkingEffort(value)
	if err != nil {
		return err
	}
	controller, ok := a.client.(llm.ThinkingEffortController)
	if !ok {
		return errors.New("the configured model protocol does not support thinking effort")
	}
	controller.SetThinkingEffort(effort)
	return nil
}

func (a *Agent) ThinkingEffort() (string, error) {
	controller, ok := a.client.(llm.ThinkingEffortController)
	if !ok {
		return "", errors.New("the configured model protocol does not support thinking effort")
	}
	return string(controller.ThinkingEffort()), nil
}

// Run executes the agent loop and emits events for upper-layer UI.
func (a *Agent) Run(conversationContext *contextmanager.ConversationContext) <-chan AgentEvent {
	return a.RunContext(agentContext(a), conversationContext)
}

// RunContext executes one agent turn with a caller-controlled context. This
// lets an interactive UI interrupt an in-flight model request or tool call.
func (a *Agent) RunContext(ctx context.Context, conversationContext *contextmanager.ConversationContext) <-chan AgentEvent {
	var budget RunBudget
	if a != nil {
		budget = a.RunBudget
	}
	return a.RunContextWithBudget(ctx, conversationContext, budget)
}

// RunContextWithBudget executes one turn with caller-supplied resource limits.
func (a *Agent) RunContextWithBudget(ctx context.Context, conversationContext *contextmanager.ConversationContext, budget RunBudget) <-chan AgentEvent {
	agentEventCh := make(chan AgentEvent, 32)
	if ctx == nil {
		ctx = agentContext(a)
	}

	go func() {
		defer close(agentEventCh)
		// Keep the presentation/session layer synchronized without making the
		// agent depend on a Session aggregate.
		defer conversationContext.Commit()
		finished := false
		checkpointAttempted := false
		stopAttempted := false
		runCompleted := false
		lifecycleCtx := ctx
		var budgetState *runBudgetState
		var persistInterrupted func()
		finish := func(event TurnEndEvent) {
			if finished {
				return
			}
			if !checkpointAttempted && !runCompleted && lifecycleCtx.Err() != nil && persistInterrupted != nil {
				checkpointAttempted = true
				persistInterrupted()
			}
			if !stopAttempted {
				stopAttempted = true
				event = a.dispatchStopHook(lifecycleCtx, conversationContext, event)
			}
			sendTurnEndEvent(agentEventCh, event)
			finished = true
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				usage := llm.UsageInfo{}
				if budgetState != nil {
					usage = budgetState.usage
				}
				finish(turnEndFromError(fmt.Errorf("agent run panic: %v", recovered), usage))
				return
			}
			if !finished {
				finish(turnEndFromError(errors.New("agent run exited without a terminal event"), llm.UsageInfo{}))
			}
		}()
		var err error
		budgetState, err = newRunBudgetState(budget)
		if err != nil {
			finish(turnEndFromError(err, llm.UsageInfo{}))
			return
		}
		ctx, cancel := budgetState.context(ctx)
		ctx = withChildRuntime(ctx, budgetState)
		eventContext := ctx
		ctx = withAgentEventSink(ctx, func(event AgentEvent) bool {
			return sendAgentEvent(eventContext, agentEventCh, event)
		})
		lifecycleCtx = ctx
		defer cancel()
		phaseController := newRunPhaseController()
		progressTracker := newProgressTracker(a.ProgressPolicy)
		evidenceCoordinator := newRunEvidenceCoordinator(a.ChangeDetector, a.VerificationClassifier)
		fingerprinter := a.ProgressFingerprinter
		if fingerprinter == nil {
			fingerprinter = gitProgressFingerprinter{}
		}

		if err := a.validate(conversationContext); err != nil {
			finish(turnEndFromError(err, budgetState.usage))
			return
		}
		ctx = hook.WithInput(ctx, hook.Input{
			SessionID:  conversationContext.SessionID,
			Workspace:  conversationContext.Workspace,
			UserPrompt: latestUserRequest(conversationContext.History),
			Prompt:     latestUserRequest(conversationContext.History),
		})
		ctx, err = a.dispatchRunStartHooks(ctx, conversationContext)
		if err != nil {
			finish(turnEndFromError(err, budgetState.usage))
			return
		}
		recoveryGuidance, err := a.recoverCheckpoint(ctx, conversationContext, fingerprinter)
		if err != nil {
			finish(turnEndFromError(err, budgetState.usage))
			return
		}
		persistInterrupted = func() {
			a.saveInterruptedCheckpoint(conversationContext, fingerprinter)
		}
		if err := ctx.Err(); err != nil {
			finish(turnEndFromError(err, budgetState.usage))
			return
		}
		evidenceCoordinator.Start(ctx, conversationContext.Workspace)
		sendAgentEvent(ctx, agentEventCh, RunPhaseEvent{Phase: PhaseExplore, Reason: PhaseReasonRunStarted})

		for iteration := 0; iteration < a.MaxIterations; iteration++ {
			budgetTransition := phaseController.observeBudget(budgetState.snapshot(time.Now()))
			emitPhaseTransition(ctx, agentEventCh, budgetTransition)
			if budgetTransition.Changed && budgetTransition.Reason == PhaseReasonSoftBudget {
				_, warnings := evidenceCoordinator.BeforeFinalize(ctx, conversationContext.Workspace, true)
				emitQualityWarnings(ctx, agentEventCh, warnings)
			}
			toolSchemas := a.toolManager.BuildAllSchemas()
			systemPrompt := conversationContext.SystemPrompt
			if a.skillManager != nil {
				systemPrompt = appendSkillPrompt(systemPrompt, a.skillManager.CatalogPrompt(), a.skillManager.Instructions())
				toolSchemas = filterSkillTools(toolSchemas, a.skillManager.AllowedTools())
			}
			systemPrompt = appendSkillPrompt(systemPrompt, runPhaseGuidance(phaseController.phase()))
			systemPrompt = appendSkillPrompt(systemPrompt, progressTracker.convergenceGuidance())
			systemPrompt = appendSkillPrompt(systemPrompt, recoveryGuidance)
			history := conversationContext.History
			var contextView *contextmanager.ContextView
			if a.contextManager != nil {
				// 每次模型请求（包括同一 Turn 中的工具循环）都重新构建 ContextView。
				// Build 内部通过同步游标避免重复写 transcript，并通过摘要检查点避免重复压缩。
				view, err := a.contextManager.Build(ctx, contextmanager.BuildInput{
					Context: conversationContext, SystemPrompt: systemPrompt, CurrentRequest: latestUserRequest(conversationContext.History),
					AvailableTools: toolSchemas,
				})
				if err != nil {
					finish(turnEndFromError(err, budgetState.usage))
					return
				}
				contextView = view
				// 从这里开始，LLM 只接触经过预算治理的视图，不再直接接触完整 History。
				systemPrompt = view.SystemPrompt
				history = view.Messages
				toolSchemas = view.Tools
			}
			var assistantText, stopReason string
			var thinkingBlocks []conversation.ThinkingBlock
			var toolCalls []llm.ToolCallComplete
			for attempt := 0; ; attempt++ {
				sendAgentEvent(ctx, agentEventCh, ThinkingStartEvent{})
				events, errs := a.client.Stream(&llm.StreamRequest{
					Context:      ctx,
					SystemPrompt: systemPrompt,
					Messages:     history,
					Tools:        toolSchemas,
				})

				result, err := a.collectStream(ctx, events, errs)
				// Provider-reported input tokens become the baseline for the next
				// ContextView; only content added after this request is estimated.
				if a.contextManager != nil {
					a.contextManager.RecordUsage(contextView, result.usage)
				}
				if budgetErr := budgetState.recordUsage(result.usage); budgetErr != nil {
					finish(turnEndFromError(budgetErr, budgetState.usage))
					return
				}
				if err == nil {
					if err := publishAttemptEvents(ctx, agentEventCh, result.events); err != nil {
						finish(turnEndFromError(err, budgetState.usage))
						return
					}
					assistantText = result.assistantText
					thinkingBlocks = result.thinkingBlocks
					toolCalls = result.toolCalls
					stopReason = result.stopReason
					break
				}
				err = llm.NormalizeProviderError("model", err)
				if !a.ProviderRetryPolicy.ShouldRetry(err, attempt) {
					finish(turnEndFromError(err, budgetState.usage))
					return
				}
				if budgetErr := budgetState.reserveProviderRetry(); budgetErr != nil {
					finish(turnEndFromError(budgetErr, budgetState.usage))
					return
				}
				delay := a.ProviderRetryPolicy.Delay(attempt, err)
				provider, errorType := providerErrorDetails(err)
				sendAgentEvent(ctx, agentEventCh, ProviderRetryEvent{Attempt: attempt + 2, Delay: delay, Provider: provider, ErrorType: errorType})
				if err := llm.WaitForRetry(ctx, delay); err != nil {
					finish(turnEndFromError(err, budgetState.usage))
					return
				}
			}

			if len(toolCalls) == 0 {
				observation, warnings := evidenceCoordinator.BeforeFinalize(ctx, conversationContext.Workspace, false)
				emitPhaseTransition(ctx, agentEventCh, phaseController.observe(observation))
				emitQualityWarnings(ctx, agentEventCh, warnings)
				if assistantText != "" || len(thinkingBlocks) > 0 {
					conversationContext.History = append(conversationContext.History, conversation.Message{
						Role:           conversation.ASSISTANT,
						Content:        assistantText,
						ThinkingBlocks: thinkingBlocks,
					})
				}
				if a.contextManager != nil {
					if err := a.contextManager.SyncContext(ctx, conversationContext); err != nil {
						finish(turnEndFromError(err, budgetState.usage))
						return
					}
				}
				if err := a.saveCheckpoint(ctx, conversationContext, fingerprinter, CheckpointCompleted, nil, nil, true); err != nil {
					finish(turnEndFromError(err, budgetState.usage))
					return
				}
				runCompleted = true
				finish(turnEndFromStopReason(stopReason, budgetState.usage))
				return
			}
			if err := budgetState.reserveToolCalls(len(toolCalls)); err != nil {
				finish(turnEndFromError(err, budgetState.usage))
				return
			}
			workspaceBefore := workspaceFingerprint(ctx, fingerprinter, conversationContext.Workspace)
			blockedDecisions := progressTracker.beforeTools(toolCalls, workspaceBefore)
			blocked := make(map[string]ProgressDecision, len(blockedDecisions))
			for _, decision := range blockedDecisions {
				blocked[decision.ToolUseID] = decision
				sendAgentEvent(ctx, agentEventCh, progressEvent(decision))
			}

			conversationContext.History = append(conversationContext.History, conversation.Message{
				Role:           conversation.ASSISTANT,
				Content:        assistantText,
				ThinkingBlocks: thinkingBlocks,
				ToolUses:       toToolUseBlocks(toolCalls),
			})
			if err := a.saveCheckpoint(ctx, conversationContext, fingerprinter, CheckpointModel, toolCalls, nil, false); err != nil {
				finish(turnEndFromError(err, budgetState.usage))
				return
			}

			toolResults, postHookErr := a.executeToolsWithBlocked(ctx, toolCalls, blocked, agentEventCh)
			conversationContext.History = append(conversationContext.History, conversation.Message{Role: conversation.TOOL, ToolResults: toolResults})
			checkpointErr := a.saveCheckpoint(ctx, conversationContext, fingerprinter, CheckpointTools, nil, completedToolIDs(toolCalls), false)
			if err := errors.Join(postHookErr, checkpointErr); err != nil {
				finish(turnEndFromError(err, budgetState.usage))
				return
			}
			workspaceAfter := workspaceFingerprint(ctx, fingerprinter, conversationContext.Workspace)
			executedCalls, executedResults := progressInputs(toolCalls, toolResults, blocked)
			emitPhaseTransition(ctx, agentEventCh, phaseController.observe(
				evidenceCoordinator.AfterTools(ctx, conversationContext.Workspace, executedCalls, executedResults),
			))
			if decision := progressTracker.observe(executedCalls, executedResults, workspaceAfter); decision.Kind != ProgressNone {
				sendAgentEvent(ctx, agentEventCh, progressEvent(decision))
			}
			if len(blockedDecisions) > 0 {
				decision := progressTracker.recordBlocked()
				if decision.Kind != ProgressNone {
					sendAgentEvent(ctx, agentEventCh, progressEvent(decision))
				}
				switch decision.Kind {
				case ProgressFinalize:
					emitPhaseTransition(ctx, agentEventCh, phaseController.transition(PhaseFinalize, PhaseReasonNoProgress))
				case ProgressStop:
					if err := a.saveCheckpoint(ctx, conversationContext, fingerprinter, CheckpointCompleted, nil, completedToolIDs(toolCalls), true); err != nil {
						finish(turnEndFromError(err, budgetState.usage))
						return
					}
					runCompleted = true
					finish(TurnEndEvent{Status: TurnIncomplete, StopReason: StopNoProgress, ProviderReason: string(StopNoProgress), Usage: budgetState.usage})
					return
				case ProgressNone, ProgressWarning, ProgressToolBlocked:
				}
			}
		}

		err = fmt.Errorf("agent loop exceeded max iterations %d", a.MaxIterations)
		finish(turnEndFromError(err, budgetState.usage))
	}()

	return agentEventCh
}

func emitPhaseTransition(ctx context.Context, events chan<- AgentEvent, transition phaseTransition) {
	if transition.Changed {
		sendAgentEvent(ctx, events, RunPhaseEvent{Phase: transition.To, Previous: transition.From, Reason: transition.Reason})
	}
}

func emitQualityWarnings(ctx context.Context, events chan<- AgentEvent, warnings []QualityWarning) {
	for _, warning := range warnings {
		sendAgentEvent(ctx, events, QualityWarningEvent{
			Code: warning.Code, Severity: warning.Severity,
			Message: warning.Message, Evidence: append([]string(nil), warning.Evidence...),
		})
	}
}

func phaseToolResults(results []conversation.ToolResultBlock) []tool.ToolResult {
	converted := make([]tool.ToolResult, len(results))
	for index, result := range results {
		converted[index] = tool.ToolResult{Output: result.Content, IsError: result.IsError}
	}
	return converted
}

func progressEvent(decision ProgressDecision) ProgressEvent {
	return ProgressEvent{Kind: decision.Kind, Repetition: decision.Repetition, ToolUseID: decision.ToolUseID, Message: decision.Message}
}

func progressInputs(calls []llm.ToolCallComplete, results []conversation.ToolResultBlock, blocked map[string]ProgressDecision) ([]llm.ToolCallComplete, []tool.ToolResult) {
	executedCalls := make([]llm.ToolCallComplete, 0, len(calls))
	executedResults := make([]tool.ToolResult, 0, len(results))
	for index, call := range calls {
		if _, isBlocked := blocked[call.ToolID]; isBlocked || index >= len(results) {
			continue
		}
		executedCalls = append(executedCalls, call)
		executedResults = append(executedResults, tool.ToolResult{Output: results[index].Content, IsError: results[index].IsError})
	}
	return executedCalls, executedResults
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
	case errors.As(err, new(*llm.ProviderError)):
		result.StopReason = StopProviderError
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

func (a *Agent) run(conversationContext *contextmanager.ConversationContext) <-chan AgentEvent {
	return a.Run(conversationContext)
}

func (a *Agent) validate(conversationContext *contextmanager.ConversationContext) error {
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
	if conversationContext == nil {
		return errors.New("conversation context cannot be nil")
	}
	if a.MaxIterations <= 0 {
		return errors.New("max iterations must be greater than zero")
	}
	return nil
}

type modelAttempt struct {
	assistantText  string
	thinkingBlocks []conversation.ThinkingBlock
	toolCalls      []llm.ToolCallComplete
	stopReason     string
	usage          llm.UsageInfo
	events         []AgentEvent
}

func (a *Agent) collectStream(ctx context.Context, events <-chan llm.StreamEvent, errs <-chan error) (modelAttempt, error) {
	var assistantText strings.Builder
	var thinkingBlocks []conversation.ThinkingBlock
	var toolCalls []llm.ToolCallComplete
	var bufferedEvents []AgentEvent
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
				bufferedEvents = append(bufferedEvents, TextEvent{Text: ev.Text})
			case llm.ThinkingStream:
				bufferedEvents = append(bufferedEvents, ThinkingEvent{Text: ev.Text})
			case llm.ThinkingComplete:
				thinkingBlocks = append(thinkingBlocks, conversation.ThinkingBlock{Thinking: ev.Thinking, Signature: ev.Signature})
			case llm.ToolCallStart:
				bufferedEvents = append(bufferedEvents, ToolCallStartEvent{ToolUseID: ev.ToolID, ToolName: ev.ToolName})
			case llm.ToolCallStream:
				bufferedEvents = append(bufferedEvents, ToolCallDeltaEvent{ToolUseID: ev.ToolID, Text: ev.Text})
			case llm.ToolCallComplete:
				toolCalls = append(toolCalls, ev)
				bufferedEvents = append(bufferedEvents, ToolCallCompleteEvent{ToolUseID: ev.ToolID, ToolName: ev.ToolName, Arguments: ev.Arguments})
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
				return modelAttempt{assistantText: assistantText.String(), thinkingBlocks: thinkingBlocks, toolCalls: toolCalls, stopReason: stopReason, usage: usage, events: bufferedEvents}, err
			}
		case <-ctx.Done():
			return modelAttempt{assistantText: assistantText.String(), thinkingBlocks: thinkingBlocks, toolCalls: toolCalls, stopReason: stopReason, usage: usage, events: bufferedEvents}, ctx.Err()
		}
	}

	return modelAttempt{assistantText: assistantText.String(), thinkingBlocks: thinkingBlocks, toolCalls: toolCalls, stopReason: stopReason, usage: usage, events: bufferedEvents}, nil
}

func publishAttemptEvents(ctx context.Context, output chan<- AgentEvent, events []AgentEvent) error {
	for _, event := range events {
		if !sendAgentEvent(ctx, output, event) {
			return ctx.Err()
		}
	}
	return nil
}

func providerErrorDetails(err error) (string, string) {
	var providerErr *llm.ProviderError
	if !errors.As(err, &providerErr) {
		return "", ""
	}
	return providerErr.Provider, providerErr.ErrorType
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

// executeTools delegates scheduling to ToolsManager and maps ordered results
// back to the model's tool-use IDs.
func (a *Agent) executeTools(ctx context.Context, calls []llm.ToolCallComplete, events chan<- AgentEvent) []conversation.ToolResultBlock {
	results, _ := a.executeToolsWithBlocked(ctx, calls, nil, events)
	return results
}

func (a *Agent) executeToolsWithBlocked(ctx context.Context, calls []llm.ToolCallComplete, blocked map[string]ProgressDecision, events chan<- AgentEvent) ([]conversation.ToolResultBlock, error) {
	invocations := make([]tool.Invocation, 0, len(calls))
	invocationIndexes := make([]int, 0, len(calls))
	for index, call := range calls {
		if _, isBlocked := blocked[call.ToolID]; isBlocked {
			continue
		}
		sendAgentEvent(ctx, events, ToolExecutionStartEvent{
			ToolUseID: call.ToolID,
			ToolName:  call.ToolName,
		})
		invocations = append(invocations, tool.Invocation{ID: call.ToolID, Name: call.ToolName, Arguments: call.Arguments})
		invocationIndexes = append(invocationIndexes, index)
	}

	results := make([]conversation.ToolResultBlock, len(calls))
	for index, call := range calls {
		if decision, isBlocked := blocked[call.ToolID]; isBlocked {
			results[index] = conversation.ToolResultBlock{
				ToolUseID: call.ToolID,
				Content:   decision.Message,
				IsError:   true,
			}
			sendAgentEvent(ctx, events, ToolResultEvent{
				ToolUseID: call.ToolID,
				ToolName:  call.ToolName,
				Content:   decision.Message,
				IsError:   true,
			})
			continue
		}
		results[index] = conversation.ToolResultBlock{
			ToolUseID: call.ToolID,
			Content:   "tool execution canceled",
			IsError:   true,
		}
	}
	executed := a.toolManager.ExecuteBatch(ctx, invocations)
	var postHookErrors []error
	for resultIndex, outcome := range executed {
		callIndex := invocationIndexes[resultIndex]
		call := calls[callIndex]
		results[callIndex] = conversation.ToolResultBlock{
			ToolUseID: call.ToolID,
			Content:   outcome.Output,
			IsError:   outcome.IsError,
		}
		sendAgentEvent(ctx, events, ToolResultEvent{
			ToolUseID: call.ToolID,
			ToolName:  call.ToolName,
			Content:   outcome.Output,
			IsError:   outcome.IsError,
		})
		if outcome.HookError != nil {
			postHookErrors = append(postHookErrors, fmt.Errorf("tool %s (%s) post hook: %w", call.ToolName, call.ToolID, outcome.HookError))
		}
	}
	return results, errors.Join(postHookErrors...)
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
