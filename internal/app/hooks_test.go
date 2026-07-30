package app

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	appconfig "FFCode/internal/config"
	"FFCode/internal/hook"
)

func TestWorkspaceHooksRequireExplicitOptIn(t *testing.T) {
	workspace := t.TempDir()
	configDir := filepath.Join(workspace, ".agent")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "hooks.yaml"), []byte("hooks:\n  session_start: 'printf triggered'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	disabled, err := loadHooks(appconfig.Config{}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	result, err := disabled.Dispatch(context.Background(), hook.EventSessionStart, hook.Input{Workspace: workspace})
	if err != nil || result.Output != "" {
		t.Fatalf("disabled workspace hook ran: result=%+v err=%v", result, err)
	}

	enabled, err := loadHooks(appconfig.Config{Hooks: appconfig.HooksConfig{Enabled: true}}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	result, err = enabled.Dispatch(context.Background(), hook.EventSessionStart, hook.Input{Workspace: workspace})
	if err != nil || result.Output != "triggered" {
		t.Fatalf("enabled workspace hook result=%+v err=%v", result, err)
	}
}

func TestExplicitHookConfigEnablesHooksWithoutWorkspaceOptIn(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "explicit-hooks.yaml")
	if err := os.WriteFile(path, []byte("hooks:\n  session_start: 'printf explicit'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MYCODE_HOOK_CONFIG", path)

	dispatcher, err := loadHooks(appconfig.Config{}, workspace)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	if err := dispatcher.Register(hook.EventSessionStart, func(hook.Input) { calls.Add(1) }); err != nil {
		t.Fatal(err)
	}
	result, err := dispatcher.Dispatch(context.Background(), hook.EventSessionStart, hook.Input{Workspace: workspace})
	if err != nil || result.Output != "explicit" || calls.Load() != 1 {
		t.Fatalf("explicit hook result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}
