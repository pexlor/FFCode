package agent

import (
	"reflect"
	"sort"
	"testing"
)

func TestQualityGateRules(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*RunEvidence)
		want    string
	}{
		{name: "source without verification", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}})
		}, want: "QG001"},
		{name: "latest verification failed", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}})
			e.RecordVerification(VerificationEvidence{ToolUseID: "test-1", Command: "go test ./...", Scope: VerificationFull, Passed: false})
		}, want: "QG002"},
		{name: "tests only", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent_test.go", Kind: ChangeTest, Operation: ChangeModified}})
		}, want: "QG003"},
		{name: "test expectation changed", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent_test.go", Kind: ChangeTest, Operation: ChangeModified, TestExpectationChanged: true}})
		}, want: "QG004"},
		{name: "test deleted", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "old_test.go", Kind: ChangeTest, Operation: ChangeDeleted}})
		}, want: "QG004"},
		{name: "fallback only", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}})
			e.RecordVerification(VerificationEvidence{ToolUseID: "check-1", Command: "git diff --check", Scope: VerificationFallback, Passed: true})
		}, want: "QG005"},
		{name: "changed after verification", prepare: func(e *RunEvidence) {
			e.RecordChanges([]WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}})
			e.RecordVerification(VerificationEvidence{ToolUseID: "test-1", Command: "go test ./...", Scope: VerificationFull, Passed: true})
			e.RecordChanges([]WorkspaceChange{{Path: "run_phase.go", Kind: ChangeSource, Operation: ChangeModified}})
		}, want: "QG006"},
		{name: "diff unavailable", prepare: func(e *RunEvidence) {
			e.DiffAvailable = false
		}, want: "QG007"},
		{name: "long empty run", prepare: func(e *RunEvidence) {
			e.ToolExecutions = 20
		}, want: "QG008"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := newRunEvidence()
			test.prepare(evidence)
			gate := newQualityGate()
			warnings := gate.Evaluate(*evidence)
			if !containsWarningCode(warnings, test.want) {
				t.Fatalf("warnings = %+v, want %s", warnings, test.want)
			}
			if repeated := gate.Evaluate(*evidence); len(repeated) != 0 {
				t.Fatalf("duplicate warnings = %+v", repeated)
			}
		})
	}
}

func TestQualityGateAvoidsUnsupportedWarnings(t *testing.T) {
	t.Run("docs only", func(t *testing.T) {
		evidence := newRunEvidence()
		evidence.RecordChanges([]WorkspaceChange{{Path: "README.md", Kind: ChangeDocs, Operation: ChangeModified}})
		if warnings := newQualityGate().Evaluate(*evidence); containsWarningCode(warnings, "QG001") {
			t.Fatalf("warnings = %+v", warnings)
		}
	})

	t.Run("failure followed by success", func(t *testing.T) {
		evidence := newRunEvidence()
		evidence.RecordChanges([]WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}})
		evidence.RecordVerification(VerificationEvidence{ToolUseID: "test-1", Scope: VerificationFull, Passed: false})
		evidence.RecordVerification(VerificationEvidence{ToolUseID: "test-2", Scope: VerificationFull, Passed: true})
		if warnings := newQualityGate().Evaluate(*evidence); containsWarningCode(warnings, "QG002") {
			t.Fatalf("warnings = %+v", warnings)
		}
	})
}

func TestQualityGateSortsEvidencePaths(t *testing.T) {
	evidence := newRunEvidence()
	evidence.RecordChanges([]WorkspaceChange{
		{Path: "z_test.go", Kind: ChangeTest, Operation: ChangeDeleted},
		{Path: "a_test.go", Kind: ChangeTest, Operation: ChangeModified, TestExpectationChanged: true},
	})
	warnings := newQualityGate().Evaluate(*evidence)
	for _, warning := range warnings {
		if warning.Code != "QG004" {
			continue
		}
		want := append([]string(nil), warning.Evidence...)
		sort.Strings(want)
		if !reflect.DeepEqual(warning.Evidence, want) {
			t.Fatalf("evidence = %v, want sorted %v", warning.Evidence, want)
		}
		return
	}
	t.Fatal("QG004 warning not found")
}

func containsWarningCode(warnings []QualityWarning, code string) bool {
	for _, warning := range warnings {
		if warning.Code == code {
			return true
		}
	}
	return false
}
