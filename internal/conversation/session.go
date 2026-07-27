package conversation

import "context"

// 工具调用
type ToolUseBlock struct {
	ToolUseID string
	ToolName  string
	Arguments map[string]any // 工具参数
}

// 工具调用结果
type ToolResultBlock struct {
	ToolUseID string
	Content   string // 包括 error信息和正常调用结果
	IsError   bool
}

// 思考内容
type ThinkingBlock struct {
	Thinking  string
	Signature string
}

type Message struct {
	Role           string            // 消息角色
	Content        string            // 普通消息内容
	ThinkingBlocks []ThinkingBlock   // 思考消息内容
	ToolUses       []ToolUseBlock    // 工具调用内容
	ToolResults    []ToolResultBlock // 工具调用结果
}

// MemoryProvider supplies the latest committed cross-session memory.
type MemoryProvider interface {
	Summary(context.Context) (string, error)
}

func (s *Session) RefreshLongTermMemory(ctx context.Context) error {
	if s == nil || !s.useMemory || s.memoryProvider == nil {
		return nil
	}
	summary, err := s.memoryProvider.Summary(ctx)
	if err != nil {
		return err
	}
	s.LongTermMemory = summary
	return nil
}

func (s *Session) AddToolUses(toolUses []ToolUseBlock) {
	s.History = append(s.History, Message{
		Role:     ASSISTANT,
		ToolUses: toolUses,
	})
}

func (s *Session) AddToolResult(toolResults []ToolResultBlock) {
	s.History = append(s.History, Message{
		Role:        TOOL,
		ToolResults: toolResults,
	})
}

func (s *Session) AddThink(thinkingBlocks []ThinkingBlock) {
	s.History = append(s.History, Message{
		Role:           ASSISTANT,
		ThinkingBlocks: thinkingBlocks,
	})
}

func (s *Session) AddText(content string) {
	s.History = append(s.History, Message{
		Role:    USER,
		Content: content,
	})
}
