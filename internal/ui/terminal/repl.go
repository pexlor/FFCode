package terminal

import (
	"MyCode/internal/agent"
	session "MyCode/internal/conversation"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/styles"
	"charm.land/lipgloss/v2"
)

type Runtime struct {
	ModelName       string
	Workspace       string
	Runner          *agent.Agent
	Sessions        *session.Service
	OnSessionChange func(string)
}

// Run owns terminal interaction only. Application construction belongs to
// internal/app so this package can remain independent from storage and model
// provider setup.
func Run(runtime Runtime) error {
	if runtime.Runner == nil || runtime.Sessions == nil {
		return errors.New("terminal runtime is not initialized")
	}
	reader := bufio.NewReader(os.Stdin)
	printWelcomeTo(os.Stdout, runtime.ModelName, runtime.Workspace)
	registry, err := NewDefaultCommandRegistry()
	if err != nil {
		return fmt.Errorf("initialize commands: %w", err)
	}
	input := openLineInput(registry)
	defer input.Close()
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	if runtime.OnSessionChange != nil {
		runtime.OnSessionChange(runtime.Sessions.Current().ID)
	}
	commandContext := &CommandContext{Sessions: runtime.Sessions, In: reader, Out: os.Stdout, Registry: registry, Thinking: runtime.Runner, Clear: func(out io.Writer) {
		fmt.Fprint(out, "\033[H\033[2J")
		printWelcomeTo(out, runtime.ModelName, runtime.Workspace)
	}, OnSessionChange: runtime.OnSessionChange}

	for {
		userInput, err := input.ReadLine(promptLabel())
		if err != nil {
			if errors.Is(err, io.EOF) {
				fmt.Println(dim("bye."))
				return nil
			}
			if errors.Is(err, errPromptAborted) {
				fmt.Println(dim("bye."))
				return nil
			}
			return fmt.Errorf("read input: %w", err)
		}

		userInput = strings.TrimSpace(userInput)
		if userInput == "" {
			continue
		}

		result := registry.Execute(context.Background(), commandContext, userInput)
		if result.Handled {
			if result.Err != nil {
				printError("命令失败", result.Err)
			}
			if result.Quit {
				fmt.Println(dim("bye."))
				return nil
			}
			continue
		}

		if err := runtime.Sessions.AddUserMessage(context.Background(), userInput); err != nil {
			printError("保存消息失败", err)
			continue
		}
		renderer := newAgentEventRenderer(os.Stderr, os.Stdout)
		failed := false
		interrupted := false
		turnContext, cancelTurn := context.WithCancel(context.Background())
		events := runtime.Runner.RunContext(turnContext, runtime.Sessions.Current().Messages)
	eventLoop:
		for {
			select {
			case <-interrupts:
				cancelTurn()
				interrupted = true
				break eventLoop
			case event, ok := <-events:
				if !ok {
					break eventLoop
				}
				if err := renderer.render(event); err != nil {
					printError("执行失败", err)
					failed = true
					break eventLoop
				}
			}
		}
		cancelTurn()
		if interrupted {
			renderer.clearStatus()
			renderer.finishThinking()
			fmt.Fprintln(os.Stderr, dim("  对话已中断"))
			continue
		}
		if failed {
			continue
		}
	}
}

func handleAgentEvent(event agent.AgentEvent) error {
	return newAgentEventRenderer(os.Stderr, os.Stdout).render(event)
}

// agentEventRenderer draws transient progress on the current terminal line.
// It clears that line as soon as output arrives, so conversation-state labels
// do not become part of the transcript.
type agentEventRenderer struct {
	statusOut        io.Writer
	textOut          io.Writer
	statusVisible    bool
	thinkingVisible  bool
	assistantStarted bool
	thinking         strings.Builder
	assistantText    strings.Builder
	markdownRenderer *glamour.TermRenderer
}

func newAgentEventRenderer(statusOut, textOut io.Writer) *agentEventRenderer {
	markdownStyle := styles.DarkStyleConfig
	zeroMargin := uint(0)
	markdownStyle.Document.BlockPrefix = ""
	markdownStyle.Document.Margin = &zeroMargin
	markdownRenderer, _ := glamour.NewTermRenderer(glamour.WithStyles(markdownStyle), glamour.WithWordWrap(80))
	return &agentEventRenderer{statusOut: statusOut, textOut: textOut, markdownRenderer: markdownRenderer}
}

func (renderer *agentEventRenderer) render(event agent.AgentEvent) error {
	switch ev := event.(type) {
	case agent.TextEvent:
		renderer.clearStatus()
		renderer.finishThinking()
		renderer.assistantText.WriteString(ev.Text)
	case agent.ThinkingStartEvent:
		renderer.showStatus(conversationStatus("正在思考"))
	case agent.ThinkingEvent:
		renderer.clearStatus()
		renderer.thinking.WriteString(ev.Text)
		renderer.renderThinkingBox()
	case agent.ToolExecutionStartEvent:
		renderer.finishThinking()
		renderer.renderAssistantMarkdown()
		renderer.showStatus(toolLine("正在调用", ev.ToolName))
	case agent.ToolResultEvent:
		renderer.clearStatus()
		renderer.finishThinking()
		status := "ok"
		color := colorGreen
		if ev.IsError {
			status = "error"
			color = colorRed
		}
		fmt.Fprintf(renderer.statusOut, "%s%s%s %s%s%s\n", colorDim, toolLine("完成", ev.ToolName), colorReset, color, status, colorReset)
	case agent.DoneEvent:
		renderer.clearStatus()
		renderer.finishThinking()
		renderer.renderAssistantMarkdown()
		fmt.Fprintf(renderer.statusOut, "\n%stokens: input %d | output %d | total %d", colorDim, ev.Usage.InputTokens, ev.Usage.OutputTokens, ev.Usage.TotalTokens)
		if ev.Usage.CacheReadTokens > 0 {
			fmt.Fprintf(renderer.statusOut, " | cache read %d", ev.Usage.CacheReadTokens)
		}
		fmt.Fprint(renderer.statusOut, colorReset)
		if ev.StopReason != "" {
			fmt.Fprintf(renderer.statusOut, "\n%sdone: %s%s\n\n", colorDim, ev.StopReason, colorReset)
		} else {
			fmt.Fprintln(renderer.statusOut)
		}
	case agent.ErrorEvent:
		return ev.Err
	}
	return nil
}

func (renderer *agentEventRenderer) renderAssistantMarkdown() {
	markdown := renderer.assistantText.String()
	if markdown == "" {
		return
	}
	renderer.assistantText.Reset()
	if !renderer.assistantStarted {
		fmt.Fprint(renderer.textOut, assistantLabel())
		renderer.assistantStarted = true
	}
	if renderer.markdownRenderer == nil {
		fmt.Fprint(renderer.textOut, markdown)
		return
	}
	rendered, err := renderer.markdownRenderer.Render(markdown)
	if err != nil {
		fmt.Fprint(renderer.textOut, markdown)
		return
	}
	fmt.Fprint(renderer.textOut, rendered)
}

func (renderer *agentEventRenderer) showStatus(status string) {
	renderer.clearStatus()
	fmt.Fprintf(renderer.statusOut, "\r\033[2K%s%s%s", colorDim, status, colorReset)
	renderer.statusVisible = true
}

func (renderer *agentEventRenderer) clearStatus() {
	if renderer.statusVisible {
		fmt.Fprint(renderer.statusOut, "\r\033[2K")
		renderer.statusVisible = false
	}
}

func (renderer *agentEventRenderer) finishThinking() {
	if renderer.thinkingVisible {
		renderer.thinkingVisible = false
	}
}

// renderThinkingBox redraws a fixed-height viewport in place. The box keeps
// the terminal transcript compact while the most recent reasoning lines remain
// visible during generation.
func (renderer *agentEventRenderer) renderThinkingBox() {
	if renderer.thinkingVisible {
		fmt.Fprintf(renderer.statusOut, "\033[%dA", thinkingBoxHeight)
	}
	lines := recentThinkingLines(renderer.thinking.String(), thinkingBoxContentWidth, thinkingBoxLineCount)
	titleFill := strings.Repeat("─", thinkingBoxContentWidth-lipgloss.Width(" 思考 "))
	fmt.Fprintf(renderer.statusOut, "\r\033[2K%s┌─ 思考 %s┐%s\n", colorGray, titleFill, colorReset)
	for _, line := range lines {
		padding := strings.Repeat(" ", thinkingBoxContentWidth-lipgloss.Width(line))
		fmt.Fprintf(renderer.statusOut, "\r\033[2K%s│%s%s│%s\n", colorGray, line, padding, colorReset)
	}
	fmt.Fprintf(renderer.statusOut, "\r\033[2K%s└%s┘%s\n", colorGray, strings.Repeat("─", thinkingBoxContentWidth), colorReset)
	renderer.thinkingVisible = true
}

func recentThinkingLines(text string, width, limit int) []string {
	lines := make([]string, 0, limit)
	for _, sourceLine := range strings.Split(text, "\n") {
		current := ""
		for _, runeValue := range sourceLine {
			candidate := current + string(runeValue)
			if lipgloss.Width(candidate) > width && current != "" {
				lines = append(lines, current)
				current = string(runeValue)
			} else {
				current = candidate
			}
		}
		lines = append(lines, current)
	}
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	for len(lines) < limit {
		lines = append([]string{""}, lines...)
	}
	return lines
}

func printWelcomeTo(out io.Writer, modelName, workspace string) {
	modelName = firstNonEmpty(modelName, "not configured")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s›_%s  %s%sMyCode%s\n", colorCyan, colorReset, colorBold, colorWhite, colorReset)
	fmt.Fprintf(out, "  %s────────────────────────────────────────────────────────%s\n", colorGray, colorReset)
	fmt.Fprintf(out, "  %smodel: %s%s\n", colorGray, modelName, colorReset)
	fmt.Fprintf(out, "  %sdirectory: %s%s\n", colorGray, workspace, colorReset)
	fmt.Fprintf(out, "  %s/help for commands  ·  /exit to quit%s\n\n", colorDim, colorReset)
}

func promptLabel() string {
	// Keep the editable prompt free of ANSI bytes: line editors count prompt
	// columns, and escape sequences would shift the cursor during CJK input.
	return "› "
}

func assistantLabel() string {
	return colorCyan + colorBold + "●" + colorReset + " "
}

func printError(label string, err error) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "%s%s:%s %v\n", colorRed, label, colorReset, err)
}

func toolLine(action string, toolName string) string {
	if toolName == "" {
		toolName = "tool"
	}
	return fmt.Sprintf("  · 工具%s：%s", action, toolName)
}

func conversationStatus(status string) string {
	return "  · " + status + "…"
}

func dim(text string) string {
	return colorDim + text + colorReset
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
