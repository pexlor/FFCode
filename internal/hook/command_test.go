package hook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCommandHookReceivesJSONAndEventEnvironment(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.RegisterCommand(EventPostToolUse, CommandSpec{
		Command: `read payload; case "$payload" in *'"tool_name":"ReadFile"'*) seen=yes;; *) seen=no;; esac; printf '{"output":"%s:%s"}' "$MYCODE_HOOK_EVENT" "$seen"`,
		Shell:   true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{ToolName: "ReadFile"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Output != "post_tool_use:yes" || strings.Contains(result.Output, `"tool_name"`) {
		t.Fatalf("command output = %q", result.Output)
	}
}

func TestCommandEnvironmentReplacesInheritedValuesWithoutDuplicates(t *testing.T) {
	t.Setenv("MYCODE_HOOK_DEPTH", "99")
	t.Setenv("HOOK_TEST_MODE", "inherited")
	environment := commandEnvironment(map[string]string{
		"MYCODE_HOOK_DEPTH": "88",
		"HOOK_TEST_MODE":    "configured",
	}, Input{Event: EventPreToolUse, Depth: 2})

	want := map[string]string{
		"MYCODE_HOOK_DEPTH": "2",
		"HOOK_TEST_MODE":    "configured",
	}
	counts := make(map[string]int, len(want))
	for _, entry := range environment {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, tracked := want[key]; !tracked {
			continue
		}
		counts[key]++
		if value != want[key] {
			t.Errorf("%s = %q, want %q", key, value, want[key])
		}
	}
	for key := range want {
		if counts[key] != 1 {
			t.Errorf("%s occurrences = %d, want 1", key, counts[key])
		}
	}
}

func TestCommandHookKillsProcessGroupOnTimeout(t *testing.T) {
	dispatcher := New(Config{Timeout: 30 * time.Millisecond, FailurePolicy: FailureClosed})
	if err := dispatcher.RegisterCommand(EventPostToolUse, CommandSpec{Command: "sleep 5", Shell: true}); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if !errors.Is(err, ErrHookTimeout) || !result.Blocked {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("timeout took %v", elapsed)
	}
}

func TestCommandHookTimeoutKillsBackgroundChildren(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process groups are not available on Windows")
	}
	marker := filepath.Join(t.TempDir(), "child-survived")
	dispatcher := New(Config{Timeout: 30 * time.Millisecond, FailurePolicy: FailureClosed})
	command := fmt.Sprintf("(sleep 0.15; printf survived > %q) & wait", marker)
	if err := dispatcher.RegisterCommand(EventPostToolUse, CommandSpec{Command: command, Shell: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{}); !errors.Is(err, ErrHookTimeout) {
		t.Fatalf("error = %v", err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("background child survived timeout: stat error = %v", err)
	}
}

func TestCommandHookUsesOneStrictOutputBudget(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 24, FailurePolicy: FailureOpen})
	if err := dispatcher.RegisterCommand(EventPostToolUse, CommandSpec{
		Command: "printf 12345678901234567890; printf abcdefghijklmnopqrst >&2",
		Shell:   true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Truncated || !result.Failed() {
		t.Fatalf("result = %+v", result)
	}
	for _, output := range result.Outputs {
		if len(output.Output)+len(output.Reason) > 24 {
			t.Fatalf("captured %d bytes, limit 24: %+v", len(output.Output)+len(output.Reason), output)
		}
	}
}

func TestCommandHookRepairsRuneSplitAtOutputLimit(t *testing.T) {
	dispatcher := New(Config{MaxOutputBytes: 2, FailurePolicy: FailureOpen})
	if err := dispatcher.RegisterCommand(EventPostToolUse, CommandSpec{
		Command: `printf '\347\225\214'`,
		Shell:   true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPostToolUse, Input{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || !result.Truncated || !result.Failed() {
		t.Fatalf("result = %+v", result)
	}
	if output := result.Outputs[0].Output; len(output) > 2 || !utf8.ValidString(output) {
		t.Fatalf("command output len=%d valid=%v output=%q", len(output), utf8.ValidString(output), output)
	}
}

func TestCommandHookRejectsUnknownJSONFields(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.RegisterCommand(EventPreToolUse, CommandSpec{
		Command: `printf '{"decison":"deny"}'`,
		Shell:   true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if !errors.Is(err, ErrInvalidOutput) || !result.Blocked {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestCommandHookRejectsMalformedJSONObject(t *testing.T) {
	dispatcher := New(DefaultConfig())
	if err := dispatcher.RegisterCommand(EventPreToolUse, CommandSpec{
		Command: `printf '{"decision":"deny"'`,
		Shell:   true,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), EventPreToolUse, Input{})
	if !errors.Is(err, ErrInvalidOutput) || !result.Blocked {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
