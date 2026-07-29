package agent

import "sort"

type ChangeKind string

const (
	ChangeSource  ChangeKind = "source"
	ChangeTest    ChangeKind = "test"
	ChangeDocs    ChangeKind = "docs"
	ChangeConfig  ChangeKind = "config"
	ChangeUnknown ChangeKind = "unknown"
)

type ChangeOperation string

const (
	ChangeAdded    ChangeOperation = "added"
	ChangeModified ChangeOperation = "modified"
	ChangeDeleted  ChangeOperation = "deleted"
)

type WorkspaceChange struct {
	Path                   string
	Kind                   ChangeKind
	Operation              ChangeOperation
	TestExpectationChanged bool
}

type VerificationEvidence struct {
	ToolUseID  string
	Command    string
	Scope      VerificationScope
	Passed     bool
	AfterPatch bool
	Revision   uint64
}

type RunEvidence struct {
	Changes                  []WorkspaceChange
	Verifications            []VerificationEvidence
	DiffAvailable            bool
	ToolExecutions           int
	FinalRequested           bool
	SoftBudgetHit            bool
	LastChangeRevision       uint64
	LastVerificationRevision uint64
	revision                 uint64
}

func newRunEvidence() *RunEvidence {
	return &RunEvidence{DiffAvailable: true}
}

func (e *RunEvidence) RecordChanges(changes []WorkspaceChange) {
	changes = cloneWorkspaceChanges(changes)
	if equalWorkspaceChanges(e.Changes, changes) {
		return
	}
	e.revision++
	e.LastChangeRevision = e.revision
	e.Changes = changes
}

func (e *RunEvidence) RecordVerification(verification VerificationEvidence) {
	verification.AfterPatch = len(e.Changes) > 0
	verification.Revision = e.revision
	e.Verifications = append(e.Verifications, verification)
	if verification.AfterPatch {
		e.LastVerificationRevision = verification.Revision
	}
}

func cloneWorkspaceChanges(changes []WorkspaceChange) []WorkspaceChange {
	cloned := append([]WorkspaceChange(nil), changes...)
	sort.Slice(cloned, func(i, j int) bool {
		if cloned[i].Path != cloned[j].Path {
			return cloned[i].Path < cloned[j].Path
		}
		if cloned[i].Operation != cloned[j].Operation {
			return cloned[i].Operation < cloned[j].Operation
		}
		return cloned[i].Kind < cloned[j].Kind
	})
	return cloned
}

func equalWorkspaceChanges(left, right []WorkspaceChange) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
