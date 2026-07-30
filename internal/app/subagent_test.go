package app

import (
	"testing"

	"FFCode/internal/hook"
	"FFCode/internal/llm"
	"FFCode/internal/permission"
	"FFCode/internal/tool"
)

type subagentTestClient struct{}

func (subagentTestClient) Stream(*llm.StreamRequest) (<-chan llm.StreamEvent, <-chan error) {
	events := make(chan llm.StreamEvent)
	errs := make(chan error)
	close(events)
	close(errs)
	return events, errs
}

func TestRegisterSubagentToolAddsDelegateTask(t *testing.T) {
	manager, err := tool.NewToolsManagerForWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := registerSubagentTool(manager, subagentTestClient{}, hook.New(hook.DefaultConfig())); err != nil {
		t.Fatal(err)
	}
	registered := manager.GetTool("delegate_task")
	if registered == nil || registered.Schema().Access != tool.ToolAccessRead {
		t.Fatalf("registered tool = %+v", registered)
	}
}

func TestDefaultPolicyAllowsDelegateTask(t *testing.T) {
	policy := permission.DefaultPolicy(t.TempDir())
	allowDefaultTools(&policy)
	configured, ok := policy.Tool("delegate_task")
	if !ok || configured.Permission != permission.Allow || !configured.ReadOnly {
		t.Fatalf("delegate policy = %+v, exists = %v", configured, ok)
	}
}
