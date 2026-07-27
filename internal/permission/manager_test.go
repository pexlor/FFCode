package permission

import (
	"context"
	"testing"
)

func TestDisabledPolicyBypassesCriticalCommandChecks(t *testing.T) {
	workspace := t.TempDir()
	policy := DefaultPolicy(workspace)
	policy.Disabled = true
	manager, err := NewManager(policy)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	result, err := manager.Authorize(context.Background(), PermissionRequest{
		ToolName:         "Bash",
		Action:           "execute",
		Command:          "rm -rf /",
		WorkingDirectory: workspace,
		RiskLevel:        Critical,
	})
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	if result.Decision != Allow {
		t.Fatalf("decision = %q, want allow (%s)", result.Decision, result.Reason)
	}
}
