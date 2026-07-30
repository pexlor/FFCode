package jsonl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"FFCode/internal/agent"
	contextmanager "FFCode/internal/context"
	"FFCode/internal/conversation"
	"FFCode/internal/protocol"
)

type TurnRunner interface {
	RunContext(context.Context, *contextmanager.ConversationContext) <-chan agent.AgentEvent
}

type SessionService interface {
	AddUserMessage(context.Context, string) error
	Current() *conversation.Session
}

type Runtime struct {
	In              io.Reader
	Out             io.Writer
	Runner          TurnRunner
	Sessions        SessionService
	OnSessionChange func(string)
}

func Run(ctx context.Context, runtime Runtime) error {
	if runtime.In == nil || runtime.Out == nil || runtime.Runner == nil || runtime.Sessions == nil {
		return errors.New("jsonl runtime is not initialized")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	current := runtime.Sessions.Current()
	if runtime.OnSessionChange != nil {
		runtime.OnSessionChange(current.ID)
	}

	encoder := protocol.NewEncoder(runtime.Out)
	reader := bufio.NewReader(runtime.In)
	turnNumber := 0
	for {
		line, readErr := reader.ReadString('\n')
		request := strings.TrimSpace(line)
		if request != "" {
			turnNumber++
			turnID := fmt.Sprintf("turn-%d", turnNumber)
			if err := runtime.Sessions.AddUserMessage(ctx, request); err != nil {
				return fmt.Errorf("store user message: %w", err)
			}
			current = runtime.Sessions.Current()
			if err := encoder.EncodeTurnStarted(current.ID, turnID); err != nil {
				return fmt.Errorf("encode turn start: %w", err)
			}
			terminalSeen := false
			conversationContext, err := contextmanager.ContextFromSession(ctx, current)
			if err != nil {
				return fmt.Errorf("create conversation context: %w", err)
			}
			for event := range runtime.Runner.RunContext(ctx, conversationContext) {
				if _, ok := event.(agent.TurnEndEvent); ok {
					if terminalSeen {
						return errors.New("agent emitted multiple terminal events")
					}
					terminalSeen = true
				}
				if err := encoder.EncodeAgentEvent(current.ID, turnID, event); err != nil {
					return fmt.Errorf("encode agent event: %w", err)
				}
			}
			if !terminalSeen {
				end := agent.TurnEndEvent{
					Status: agent.TurnFailed, StopReason: agent.StopAgentError,
					Err: errors.New("agent event stream closed without terminal event"),
				}
				if err := encoder.EncodeAgentEvent(current.ID, turnID, end); err != nil {
					return fmt.Errorf("encode synthetic terminal event: %w", err)
				}
			}
		}

		switch readErr {
		case nil:
			continue
		case io.EOF:
			return nil
		default:
			return fmt.Errorf("read jsonl input: %w", readErr)
		}
	}
}
