package agent

import "testing"

func TestRunEvidenceTracksVerificationRelativeToChanges(t *testing.T) {
	evidence := newRunEvidence()
	evidence.RecordVerification(VerificationEvidence{ToolUseID: "baseline", Scope: VerificationFull, Passed: true})
	evidence.RecordChanges([]WorkspaceChange{{Path: "internal/agent/agent.go", Kind: ChangeSource, Operation: ChangeModified}})
	evidence.RecordVerification(VerificationEvidence{ToolUseID: "test-1", Scope: VerificationPackage, Passed: true})

	if evidence.Verifications[0].AfterPatch {
		t.Fatal("baseline verification counted as post-patch")
	}
	if !evidence.Verifications[1].AfterPatch {
		t.Fatal("post-patch verification was not marked")
	}
	if evidence.LastChangeRevision != evidence.LastVerificationRevision {
		t.Fatalf("revisions = change %d, verification %d", evidence.LastChangeRevision, evidence.LastVerificationRevision)
	}

	evidence.RecordChanges([]WorkspaceChange{{Path: "internal/agent/run_phase.go", Kind: ChangeSource, Operation: ChangeModified}})
	if evidence.LastChangeRevision <= evidence.LastVerificationRevision {
		t.Fatal("new change did not invalidate verification")
	}
}

func TestRunEvidenceDoesNotAdvanceRevisionForSameChanges(t *testing.T) {
	evidence := newRunEvidence()
	changes := []WorkspaceChange{{Path: "agent.go", Kind: ChangeSource, Operation: ChangeModified}}
	evidence.RecordChanges(changes)
	firstRevision := evidence.LastChangeRevision
	evidence.RecordChanges(append([]WorkspaceChange(nil), changes...))
	if evidence.LastChangeRevision != firstRevision {
		t.Fatalf("revision advanced from %d to %d", firstRevision, evidence.LastChangeRevision)
	}

	changes[0].Path = "mutated-by-caller.go"
	if evidence.Changes[0].Path != "agent.go" {
		t.Fatalf("evidence retained caller slice: %+v", evidence.Changes)
	}
}
