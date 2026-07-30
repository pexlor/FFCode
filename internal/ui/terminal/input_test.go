package terminal

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func TestPromptModelCtrlEnterInsertsNewlineAndEnterSubmits(t *testing.T) {
	model := newPromptModel("› ", nil)
	model.input.SetValue("first")
	model.input.CursorEnd()

	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	if model.submitted {
		t.Fatal("Ctrl+Enter submitted the prompt")
	}
	if got := model.input.Value(); got != "first\n" {
		t.Fatalf("value after Ctrl+Enter = %q, want %q", got, "first\n")
	}

	model.input.SetValue(model.input.Value() + "second")
	model.input.CursorEnd()
	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !model.submitted {
		t.Fatal("Enter did not submit the prompt")
	}
	if got := model.input.Value(); got != "first\nsecond" {
		t.Fatalf("submitted value = %q, want %q", got, "first\nsecond")
	}
}

func TestPromptModelDynamicHeightIncludesExplicitAndSoftWrappedLines(t *testing.T) {
	model := newPromptModel("› ", nil)
	model = updatePromptModel(t, model, tea.WindowSizeMsg{Width: 18, Height: 40})

	model.input.SetValue(strings.Repeat("line\n", 9) + "line")
	if got := lipgloss.Height(model.input.View()); got != 8 {
		t.Fatalf("height for ten explicit lines = %d, want 8", got)
	}

	model.input.SetValue(strings.Repeat("界", 20))
	if got := lipgloss.Height(model.input.View()); got <= 1 || got > 8 {
		t.Fatalf("height for soft-wrapped input = %d, want 2..8", got)
	}
}

func TestPromptModelUpMovesWithinMultilineInputBeforeHistory(t *testing.T) {
	model := newPromptModel("› ", nil, []string{"older message"})
	model.input.SetValue("first line\nsecond line")

	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := model.input.Value(); got != "first line\nsecond line" {
		t.Fatalf("value after Up = %q, want draft unchanged", got)
	}
	if model.historyAt != -1 {
		t.Fatalf("historyAt after multiline Up = %d, want -1", model.historyAt)
	}
	if got := model.input.Line(); got != 0 {
		t.Fatalf("cursor line after Up = %d, want 0", got)
	}
}

func TestPromptModelUpAtFirstVisualLineRecallsHistory(t *testing.T) {
	model := newPromptModel("› ", nil, []string{"older message"})
	model.input.SetValue("draft")

	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))

	if got := model.input.Value(); got != "older message" {
		t.Fatalf("value after boundary Up = %q, want history", got)
	}
	if model.historyAt != 0 {
		t.Fatalf("historyAt after boundary Up = %d, want 0", model.historyAt)
	}
}

func TestPromptModelSlashCommandCompletionSurvivesTextareaMigration(t *testing.T) {
	registry, err := NewDefaultCommandRegistry()
	if err != nil {
		t.Fatal(err)
	}
	model := newPromptModel("› ", registry)
	model.input.SetValue("/ex")
	model.refreshHints()

	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if got := model.input.Value(); got != "/exit" {
		t.Fatalf("value after Tab = %q, want /exit", got)
	}
	model = updatePromptModel(t, model, tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if !model.submitted || model.input.Value() != "/exit" {
		t.Fatalf("Enter completion submitted=%v value=%q", model.submitted, model.input.Value())
	}
}

func updatePromptModel(t *testing.T, model promptModel, message tea.Msg) promptModel {
	t.Helper()
	updated, _ := model.Update(message)
	result, ok := updated.(promptModel)
	if !ok {
		t.Fatalf("updated model type = %T, want promptModel", updated)
	}
	return result
}
