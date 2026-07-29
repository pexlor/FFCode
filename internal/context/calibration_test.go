package contextmanager

import (
	"testing"

	"MyCode/internal/llm"
)

func TestProviderUsageCalibratesOnlyChangedContext(t *testing.T) {
	manager := &ContextManager{calibration: make(map[string]tokenCalibration)}
	const sessionID = "session-1"

	if got := manager.calibratedEstimate(sessionID, 100); got != 100 {
		t.Fatalf("initial estimate = %d, want 100", got)
	}
	manager.RecordUsage(&ContextView{SessionID: sessionID, EstimatedTokens: 100, rawTokens: 100}, llm.UsageInfo{InputTokens: 120})

	if got := manager.calibratedEstimate(sessionID, 107); got != 127 {
		t.Fatalf("estimate after 7 added tokens = %d, want 127", got)
	}
	manager.RecordUsage(&ContextView{SessionID: sessionID, EstimatedTokens: 127, rawTokens: 107}, llm.UsageInfo{InputTokens: 130})

	if got := manager.calibratedEstimate(sessionID, 110); got != 133 {
		t.Fatalf("estimate after a later 3-token change = %d, want 133", got)
	}
}

func TestProviderUsageDoesNotReplaceBaselineWithMissingUsage(t *testing.T) {
	manager := &ContextManager{calibration: map[string]tokenCalibration{
		"session-1": {estimatedInput: 100, actualInput: 120},
	}}
	manager.RecordUsage(&ContextView{SessionID: "session-1", EstimatedTokens: 125, rawTokens: 105}, llm.UsageInfo{})

	if got := manager.calibratedEstimate("session-1", 105); got != 125 {
		t.Fatalf("estimate after missing usage = %d, want 125", got)
	}
}
