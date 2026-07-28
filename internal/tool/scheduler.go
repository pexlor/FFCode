package tool

import (
	"context"
	"sync/atomic"
)

type ToolAccess string

const (
	ToolAccessRead      ToolAccess = "read"
	ToolAccessWrite     ToolAccess = "write"
	ToolAccessExclusive ToolAccess = "exclusive"
)

type Invocation struct {
	ID        string
	Name      string
	Arguments map[string]any
}

const (
	invocationPending uint32 = iota
	invocationCommitted
	invocationCanceled
)

type scheduledInvocationState struct {
	phase atomic.Uint32
}

func (s *scheduledInvocationState) commit() bool {
	return s.phase.CompareAndSwap(invocationPending, invocationCommitted)
}

func (s *scheduledInvocationState) cancelBeforeCommit() {
	s.phase.CompareAndSwap(invocationPending, invocationCanceled)
}

func (s *scheduledInvocationState) committed() bool {
	return s.phase.Load() == invocationCommitted
}

// ToolAccess returns the registered tool's scheduling class. Missing tools
// and tools without explicit metadata are exclusive by default.
func (m *ToolsManager) ToolAccess(name string) ToolAccess {
	registered := m.GetTool(name)
	if registered == nil || registered.Schema() == nil {
		return ToolAccessExclusive
	}
	if registered.Schema().Access == ToolAccessRead {
		return ToolAccessRead
	}
	if registered.Schema().Access == ToolAccessWrite {
		return ToolAccessWrite
	}
	return ToolAccessExclusive
}

// ExecuteBatch runs consecutive reads concurrently and places a barrier
// around every write or exclusive call. Returned results retain call order.
func (m *ToolsManager) ExecuteBatch(ctx context.Context, calls []Invocation) []ToolResult {
	if ctx == nil {
		ctx = context.Background()
	}
	results := make([]ToolResult, len(calls))
	for index := range results {
		results[index] = canceledToolResult(ctx)
	}
	for index := 0; index < len(calls); {
		if ctx.Err() != nil {
			return results
		}
		if m.ToolAccess(calls[index].Name) != ToolAccessRead {
			state := &scheduledInvocationState{}
			completed := make(chan ToolResult, 1)
			go func(call Invocation) {
				completed <- m.executeInvocation(ctx, call, state)
			}(calls[index])
			select {
			case results[index] = <-completed:
			case <-ctx.Done():
				state.cancelBeforeCommit()
				if state.committed() {
					// The invocation crossed the execution boundary. Drain its real
					// result so detached post hooks finish before batch termination.
					results[index] = <-completed
				}
				return results
			}
			index++
			continue
		}

		end := index + 1
		for end < len(calls) && m.ToolAccess(calls[end].Name) == ToolAccessRead {
			end++
		}
		type completedRead struct {
			index  int
			result ToolResult
		}
		completed := make(chan completedRead, end-index)
		states := make(map[int]*scheduledInvocationState, end-index)
		for readIndex := index; readIndex < end; readIndex++ {
			state := &scheduledInvocationState{}
			states[readIndex] = state
			go func(resultIndex int, state *scheduledInvocationState) {
				completed <- completedRead{index: resultIndex, result: m.executeInvocation(ctx, calls[resultIndex], state)}
			}(readIndex, state)
		}
		for range end - index {
			select {
			case outcome := <-completed:
				results[outcome.index] = outcome.result
				delete(states, outcome.index)
			case <-ctx.Done():
				mustDrain := make(map[int]struct{})
				for resultIndex, state := range states {
					state.cancelBeforeCommit()
					if state.committed() {
						mustDrain[resultIndex] = struct{}{}
					}
				}
				for len(mustDrain) > 0 {
					outcome := <-completed
					if _, ok := mustDrain[outcome.index]; !ok {
						continue
					}
					results[outcome.index] = outcome.result
					delete(mustDrain, outcome.index)
				}
				return results
			}
		}
		index = end
	}
	return results
}

func (m *ToolsManager) executeInvocation(ctx context.Context, call Invocation, state *scheduledInvocationState) ToolResult {
	if err := ctx.Err(); err != nil {
		return ToolResult{Output: "tool execution canceled: " + err.Error(), IsError: true}
	}
	return m.executeInvocationWithCommit(ctx, call, state.commit)
}

func canceledToolResult(ctx context.Context) ToolResult {
	if err := ctx.Err(); err != nil {
		return ToolResult{Output: "tool execution canceled: " + err.Error(), IsError: true}
	}
	return ToolResult{Output: "tool execution canceled", IsError: true}
}
