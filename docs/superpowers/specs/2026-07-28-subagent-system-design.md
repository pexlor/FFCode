# Read-Only Delegated Subagent System Design

## Status

Design reviewed and confirmed by the user on 2026-07-28. Implementation plan intentionally deferred.

## Context

MyCode already separates the Agent loop from conversation, context, tools, and terminal rendering. The Agent also provides run budgets, cancellation, progress detection, checkpoints, lifecycle hooks, and tool scheduling. `subagent` and `agent team` are currently listed as future capabilities.

This design adds the first capability: a primary Agent can explicitly delegate independent, read-only analysis tasks to child Agents. It does not introduce free-form Agent-to-Agent messaging or a general workflow DAG.

## Goals

- Let the primary Agent explicitly create independent Subagent tasks through a `delegate_task` tool.
- Keep Subagent sessions and transcripts separate from the parent session.
- Enforce read-only workspace access at the tool-registration boundary.
- Make child budgets a strict subset of the parent run's remaining budget.
- Support bounded parallel execution and parent cancellation.
- Return stable, source-backed structured results to the primary Agent.
- Reuse the existing Agent loop, RunBudget, Hook, checkpoint, and tool scheduling behavior.

## Non-Goals

- Writable Subagents or automatic file merging.
- Nested delegation from a Subagent.
- Free-form communication among Subagents.
- Automatic task decomposition or a general DAG/workflow engine.
- Independent user-visible resumable Subagent sessions in version 1.
- Remote or cross-process Subagent execution.

## Architecture

```text
primary agent
  -> delegate_task tool
      -> subagent.Manager
          -> child Session
          -> read-only ToolManager
          -> child Agent
          -> bounded execution and result aggregation
  <- structured tool result
```

`internal/subagent` owns child lifecycle, isolation, budget allocation, concurrency, and result aggregation. `agent.Agent` remains unaware of whether it is running as a primary Agent or a Subagent. `delegate_task` is the only entry point and is registered as an ordinary tool.

For each call, the Manager validates the request, creates a child session with a new `subagent_id` and `session_id`, constructs a read-only tool registry, runs the child Agent with the derived budget, collects its terminal result and evidence, and serializes the result as the parent tool result. Child events are observable through the existing event and hook paths but are not appended to the parent transcript.

## Interfaces

```go
type Manager interface {
    Delegate(ctx context.Context, request Request) Result
}

type ParentBudget interface {
    Remaining() agent.RunBudget
    Reserve(requested agent.RunBudget) (agent.RunBudget, error)
    Commit(usage llm.UsageInfo, toolCalls int)
    Release()
}

type Request struct {
    ParentSessionID   string
    Workspace         string
    Task              string
    AdditionalContext string
    Budget            agent.RunBudget
    ParentBudget      ParentBudget
}

type Result struct {
    SubagentID string
    SessionID  string
    Status     Status
    Summary    string
    Evidence   []Evidence
    FilesRead  []string
    Usage      llm.UsageInfo
    StopReason agent.StopReason
    Err        error
}

type Evidence struct {
    Kind      string
    Source    string
    Content   string
    Important bool
}
```

The public tool input contains only task text, optional context, and requested budget limits. The tool result uses stable JSON with `status`, `summary`, `evidence`, `files_read`, `usage`, and `stop_reason`. Evidence must include a source path or tool origin so the primary Agent can distinguish observations from unsupported conclusions. `ParentBudget` is an internal capability passed through the tool execution context; it is never model-controlled and prevents `subagent` from depending on the private `runBudgetState` implementation.

## Read-Only Isolation

The child ToolManager contains only `ReadFile`, `Glob`, and `Grep` in version 1. Bash is disabled because arbitrary shell commands cannot be reliably classified as read-only. Write tools, permission escalation, workspace changes, and `delegate_task` are unavailable in the child schema and are rejected at execution time if requested by name.

The child workspace is fixed to the parent's active workspace and remains subject to the existing workspace path checks. Prompt text describes the constraint, but enforcement is performed by tool registration and authorization rather than by prompting alone.

## Budget and Concurrency

The effective child budget is the minimum of the requested limits and the parent's remaining limits for duration, input tokens, output tokens, and tool calls. The Manager reserves that capacity atomically before starting the child, commits actual usage at completion, and releases unused capacity. This prevents concurrent children from each observing and spending the same remaining budget. Default per-child limits are two minutes, 30 tool calls, and 4,000 output tokens. A parent run may execute at most four Subagents concurrently and eight total Subagent calls by default.

Independent calls may run in parallel behind a bounded semaphore. Results are returned in the model's original tool-call order, regardless of completion order. One child failure does not cancel independent siblings. Parent cancellation, deadline, or hard budget cutoff cancels all active children.

## Lifecycle, Events, and Hooks

Add Subagent lifecycle events carrying `SubagentID` and parent/session identity:

- `SubagentStartEvent`
- `SubagentEvent` (a wrapped child `AgentEvent`)
- `SubagentStopEvent`

Add `EventSubagentStart` and `EventSubagentStop` hook points. Start and stop events are always published. Every child `AgentEvent` is wrapped as `SubagentEvent` and sent to the configured event sink, preserving observability without confusing it with a primary Agent event. The terminal UI renders only compact child-task status in version 1; JSONL and future observers may consume the wrapped stream. Child events are never written to the primary session transcript.

## Status and Failure Semantics

| Condition | Result status | Primary Agent behavior |
| --- | --- | --- |
| Child reaches a normal final response | `completed` | Use summary and evidence |
| Model or tool error | `failed` | Retry or choose another path |
| Parent/user cancellation | `canceled` | Do not retry automatically |
| Child budget exhausted | `budget_exhausted` | Use available partial evidence |
| Concurrency or count limit | `rejected` | Reduce or serialize delegation |

Errors are returned as structured tool results and do not automatically terminate the primary Agent or sibling children. A completed child result is persisted in the parent run checkpoint so recovery does not repeat it. Version 1 does not independently resume an interrupted child.

## Proposed Package Surface

```text
internal/subagent/
  manager.go
  request.go
  result.go
  readonly.go
  events.go
  checkpoint.go
```

Likely integration points are `internal/agent/events.go`, `internal/agent/hooks.go`, `internal/app/bootstrap.go`, and tool registration. The Agent's main loop should remain unchanged apart from registering and forwarding the delegate capability.

## Testing and Verification

Tests must cover read-only schema filtering, nested delegation rejection, child budget clamping, parent cancellation, bounded concurrency, result ordering, checkpoint deduplication, stable JSON serialization, and event identity. Because concurrency and cancellation are involved, focused packages must also run with the race detector.

Required verification after implementation:

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./internal/subagent ./internal/agent ./internal/tool
```

## Acceptance Criteria

1. The primary Agent can invoke `delegate_task` with a task and optional budget.
2. A child can inspect the workspace but cannot write, escalate permissions, or delegate again.
3. Multiple independent children respect configured concurrency and return ordered results.
4. Parent cancellation and budget limits propagate deterministically.
5. Child results are source-backed, structured, and isolated from the parent transcript.
6. Existing primary Agent behavior and terminal UI remain compatible when no delegation occurs.
