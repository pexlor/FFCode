package llm

import (
	"MyCode/internal/conversation"
	"MyCode/internal/tool"
	"context"
	"fmt"
	"strings"
)

type ThinkingEffort string

const (
	ThinkingEffortOff     ThinkingEffort = "off"
	ThinkingEffortMinimal ThinkingEffort = "minimal"
	ThinkingEffortLow     ThinkingEffort = "low"
	ThinkingEffortMedium  ThinkingEffort = "medium"
	ThinkingEffortHigh    ThinkingEffort = "high"
	ThinkingEffortXHigh   ThinkingEffort = "xhigh"
)

func ParseThinkingEffort(value string) (ThinkingEffort, error) {
	effort := ThinkingEffort(strings.ToLower(strings.TrimSpace(value)))
	switch effort {
	case ThinkingEffortOff, ThinkingEffortMinimal, ThinkingEffortLow, ThinkingEffortMedium, ThinkingEffortHigh, ThinkingEffortXHigh:
		return effort, nil
	default:
		return "", fmt.Errorf("invalid thinking effort %q", value)
	}
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments string
}

type StreamRequest struct {
	Context      context.Context
	SystemPrompt string
	Messages     []conversation.Message
	Tools        []*tool.ToolSchema
}

type LLMClient interface {
	Stream(req *StreamRequest) (<-chan StreamEvent, <-chan error)
}

// ThinkingModeController is implemented by providers whose thinking mode can
// be changed between requests.
type ThinkingModeController interface {
	SetThinkingEnabled(enabled bool)
	ThinkingEnabled() bool
}

type ThinkingEffortController interface {
	SetThinkingEffort(effort ThinkingEffort)
	ThinkingEffort() ThinkingEffort
}

// 对话模型参数
type ModelParm struct {
	Protocol  string
	ModelName string
	Provider  string

	BaseURL string
	APIKey  string

	TopK float64
	TopP float64
	Temp float64

	EnableThinking bool
	ThinkingEffort ThinkingEffort
	ThinkingBudget int64

	MaxToken      int64
	ContextWindow int64
}

func NewClient(parm *ModelParm) (LLMClient, error) {
	if parm == nil {
		return nil, fmt.Errorf("%w: model parameters cannot be nil", ErrInvalidConfig)
	}
	switch parm.Protocol {
	case "openai-compat":
		return newOpenAiCompatClient(parm)
	case "anthropic":
		return newAnthropicClient(parm)
	default:
		return nil, fmt.Errorf("unknown model protocol: %s", parm.Protocol)
	}
}
