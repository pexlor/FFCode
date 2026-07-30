package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var errPromptAborted = errors.New("prompt aborted")

type lineInput interface {
	ReadLine(prompt string) (string, error)
	Close() error
}

type streamLineInput struct{ reader *bufio.Reader }

func newStreamLineInput(reader io.Reader) *streamLineInput {
	return &streamLineInput{reader: bufio.NewReader(reader)}
}

func (input *streamLineInput) ReadLine(_ string) (string, error) {
	line, err := input.reader.ReadString('\n')
	if err != nil && !(errors.Is(err, io.EOF) && line != "") {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func (*streamLineInput) Close() error { return nil }

func openLineInput(registry *CommandRegistry) lineInput {
	info, err := os.Stdin.Stat()
	if err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return newTerminalLineInput(registry)
	}
	return newStreamLineInput(os.Stdin)
}

type commandHint struct {
	Name        string
	Usage       string
	Description string
}

func commandHints(registry *CommandRegistry, value string) []commandHint {
	if registry == nil || !strings.HasPrefix(value, "/") || strings.ContainsAny(value, " \t\r\n") {
		return nil
	}
	prefix := strings.ToLower(strings.TrimPrefix(value, "/"))
	hints := make([]commandHint, 0, len(registry.ordered))
	for _, command := range registry.ordered {
		if strings.HasPrefix(command.Name, prefix) {
			hints = append(hints, commandHint{
				Name:        command.Name,
				Usage:       command.Usage,
				Description: command.Description,
			})
		}
	}
	return hints
}

type terminalLineInput struct {
	registry *CommandRegistry
	history  []string
}

func newTerminalLineInput(registry *CommandRegistry) *terminalLineInput {
	return &terminalLineInput{registry: registry}
}

func (input *terminalLineInput) ReadLine(prompt string) (string, error) {
	model := newPromptModel(prompt, input.registry, input.history)
	program := tea.NewProgram(model, tea.WithInput(os.Stdin), tea.WithOutput(os.Stdout))
	result, err := program.Run()
	if err != nil {
		return "", err
	}
	finalModel, ok := result.(promptModel)
	if !ok {
		return "", errors.New("unexpected prompt state")
	}
	if finalModel.aborted {
		return "", errPromptAborted
	}
	value := finalModel.input.Value()
	input.remember(value)
	return value, nil
}

func (*terminalLineInput) Close() error { return nil }

func (input *terminalLineInput) remember(value string) {
	value = strings.TrimSpace(value)
	if value == "" || (len(input.history) > 0 && input.history[len(input.history)-1] == value) {
		return
	}
	input.history = append(input.history, value)
	const historyLimit = 200
	if len(input.history) > historyLimit {
		input.history = append([]string(nil), input.history[len(input.history)-historyLimit:]...)
	}
}

type promptModel struct {
	input     textarea.Model
	registry  *CommandRegistry
	hints     []commandHint
	selected  int
	dismissed bool
	submitted bool
	aborted   bool
	width     int
	history   []string
	historyAt int
	draft     string
}

func newPromptModel(prompt string, registry *CommandRegistry, histories ...[]string) promptModel {
	input := textarea.New()
	input.Prompt = prompt
	input.ShowLineNumbers = false
	input.DynamicHeight = true
	input.MinHeight = 1
	input.MaxHeight = 8
	input.MaxContentHeight = 10_000
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("ctrl+enter"))
	styles := input.Styles()
	styles.Focused.Prompt = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	styles.Focused.Text = lipgloss.NewStyle().Foreground(lipgloss.Color("15"))
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Cursor.Color = lipgloss.Color("14")
	input.SetStyles(styles)
	// A real terminal cursor allows macOS and other system IMEs to anchor
	// their candidate window at the actual insertion point.
	input.SetVirtualCursor(false)
	input.SetHeight(1)
	input.SetWidth(max(12, 100-lipgloss.Width(prompt)-1))
	input.Focus()
	var history []string
	if len(histories) > 0 {
		history = append(history, histories[0]...)
	}
	return promptModel{input: input, registry: registry, width: 100, history: history, historyAt: -1}
}

func (model promptModel) Init() tea.Cmd { return nil }

func (model promptModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := message.(tea.WindowSizeMsg); ok {
		model.width = size.Width
		model.input.SetWidth(max(12, size.Width-lipgloss.Width(model.input.Prompt)-1))
	}

	if key, ok := message.(tea.KeyPressMsg); ok {
		switch key.String() {
		case "ctrl+c":
			model.aborted = true
			return model, tea.Quit
		case "enter":
			if len(model.hints) > 0 {
				hint := model.hints[model.selected]
				value := "/" + hint.Name
				if commandRequiresArguments(hint) {
					model.input.SetValue(value + " ")
					model.input.CursorEnd()
					model.dismissed = true
					model.hints = nil
					return model, nil
				}
				model.input.SetValue(value)
				model.input.CursorEnd()
			}
			model.submitted = true
			return model, tea.Quit
		case "esc":
			model.dismissed = true
			model.hints = nil
			return model, nil
		case "up":
			if len(model.hints) > 0 {
				model.selected = (model.selected - 1 + len(model.hints)) % len(model.hints)
				return model, nil
			}
			if model.atFirstVisualLine() && model.recallHistory(-1) {
				return model, nil
			}
		case "down":
			if len(model.hints) > 0 {
				model.selected = (model.selected + 1) % len(model.hints)
				return model, nil
			}
			if model.atLastVisualLine() && model.recallHistory(1) {
				return model, nil
			}
		case "tab":
			if len(model.hints) > 0 {
				model.input.SetValue("/" + model.hints[model.selected].Name)
				model.input.CursorEnd()
				model.dismissed = false
				model.refreshHints()
				return model, nil
			}
		}
	}

	previous := model.input.Value()
	var command tea.Cmd
	model.input, command = model.input.Update(message)
	if model.input.Value() != previous {
		model.historyAt = -1
		model.dismissed = false
		model.selected = 0
		model.refreshHints()
	}
	return model, command
}

func commandRequiresArguments(hint commandHint) bool {
	return strings.Contains(strings.TrimSpace(hint.Usage), " ")
}

func (model promptModel) atFirstVisualLine() bool {
	line := model.input.LineInfo()
	return model.input.Line() == 0 && line.RowOffset == 0
}

func (model promptModel) atLastVisualLine() bool {
	line := model.input.LineInfo()
	return model.input.Line() == model.input.LineCount()-1 && line.RowOffset == line.Height-1
}

// recallHistory returns true when the key was consumed. A fresh draft is kept
// while browsing so Down restores it after the newest history item.
func (model *promptModel) recallHistory(direction int) bool {
	if len(model.history) == 0 {
		return false
	}
	if direction < 0 {
		if model.historyAt == -1 {
			model.draft = model.input.Value()
			model.historyAt = len(model.history) - 1
		} else if model.historyAt > 0 {
			model.historyAt--
		}
	} else {
		if model.historyAt == -1 {
			return false
		}
		if model.historyAt < len(model.history)-1 {
			model.historyAt++
		} else {
			model.historyAt = -1
			model.input.SetValue(model.draft)
			model.input.CursorEnd()
			model.dismissed = true
			model.hints = nil
			return true
		}
	}
	model.input.SetValue(model.history[model.historyAt])
	model.input.CursorEnd()
	model.dismissed = true
	model.hints = nil
	return true
}

func (model *promptModel) refreshHints() {
	if model.dismissed {
		model.hints = nil
		return
	}
	model.hints = commandHints(model.registry, model.input.Value())
	if model.selected >= len(model.hints) {
		model.selected = 0
	}
}

func (model promptModel) View() tea.View {
	if model.aborted {
		return tea.NewView("")
	}
	if model.submitted {
		return tea.NewView(model.input.Prompt + model.input.Value() + "\n")
	}

	var view strings.Builder
	view.WriteString(model.input.View())
	if len(model.hints) == 0 {
		result := tea.NewView(view.String())
		result.Cursor = model.input.Cursor()
		return result
	}
	view.WriteByte('\n')
	limit := min(8, len(model.hints))
	for index, hint := range model.hints[:limit] {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
		if index == model.selected {
			marker = "› "
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("14"))
		}
		line := fmt.Sprintf("%s%-18s %s", marker, hint.Usage, hint.Description)
		if model.width > 0 && lipgloss.Width(line) > model.width {
			line = lipgloss.NewStyle().MaxWidth(model.width).Render(line)
		}
		view.WriteString(style.Render(line))
		if index < limit-1 {
			view.WriteByte('\n')
		}
	}
	if len(model.hints) > limit {
		fmt.Fprintf(&view, "\n%s还有 %d 个命令%s", colorDim, len(model.hints)-limit, colorReset)
	}
	view.WriteString("\n")
	view.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Render("  ↑↓ 选择 · Enter 确认 · Tab 补全 · Esc 收起"))
	result := tea.NewView(view.String())
	result.Cursor = model.input.Cursor()
	return result
}

var _ lineInput = (*terminalLineInput)(nil)
var _ io.Closer = (*terminalLineInput)(nil)
