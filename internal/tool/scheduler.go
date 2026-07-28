package tool

import (
	"context"
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
		if m.ToolAccess(calls[index].Name) != ToolAccessRead {
			completed := make(chan ToolResult, 1)
			go func(call Invocation) {
				completed <- m.executeInvocation(ctx, call)
			}(calls[index])
			select {
			case results[index] = <-completed:
			case <-ctx.Done():
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
		for readIndex := index; readIndex < end; readIndex++ {
			go func(resultIndex int) {
				completed <- completedRead{index: resultIndex, result: m.executeInvocation(ctx, calls[resultIndex])}
			}(readIndex)
		}
		for range end - index {
			select {
			case outcome := <-completed:
				results[outcome.index] = outcome.result
			case <-ctx.Done():
				return results
			}
		}
		index = end
	}
	return results
}

func (m *ToolsManager) executeInvocation(ctx context.Context, call Invocation) ToolResult {
	if err := ctx.Err(); err != nil {
		return ToolResult{Output: "tool execution canceled: " + err.Error(), IsError: true}
	}
	return m.Execute(ctx, call.Name, call.Arguments)
}

func canceledToolResult(ctx context.Context) ToolResult {
	if err := ctx.Err(); err != nil {
		return ToolResult{Output: "tool execution canceled: " + err.Error(), IsError: true}
	}
	return ToolResult{Output: "tool execution canceled", IsError: true}
}
