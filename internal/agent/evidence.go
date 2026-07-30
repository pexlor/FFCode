package agent

import (
	"context"
	"sort"

	"FFCode/internal/llm"
	"FFCode/internal/tool"
)

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

type runEvidenceCoordinator struct {
	detector       ChangeDetector
	classifier     VerificationClassifier
	evidence       *RunEvidence
	gate           *qualityGate
	baseline       WorkspaceSnapshot
	baselineLoaded bool
}

func newRunEvidenceCoordinator(detector ChangeDetector, classifier VerificationClassifier) *runEvidenceCoordinator {
	if detector == nil {
		detector = newGitChangeDetector(defaultDiffLimit)
	}
	if classifier == nil {
		classifier = defaultVerificationClassifier{}
	}
	return &runEvidenceCoordinator{
		detector: detector, classifier: classifier,
		evidence: newRunEvidence(), gate: newQualityGate(),
	}
}

func (c *runEvidenceCoordinator) Start(ctx context.Context, workspace string) {
	baseline, err := c.detector.Snapshot(ctx, workspace)
	if err != nil {
		c.evidence.DiffAvailable = false
		return
	}
	c.baseline = baseline
	c.baselineLoaded = true
	c.evidence.DiffAvailable = baseline.Complete
}

func (c *runEvidenceCoordinator) AfterTools(ctx context.Context, workspace string, calls []llm.ToolCallComplete, results []tool.ToolResult) phaseObservation {
	previousRevision := c.evidence.LastChangeRevision
	c.refresh(ctx, workspace)
	observation := phaseObservation{
		WorkspaceChanged: c.evidence.LastChangeRevision > previousRevision && len(c.evidence.Changes) > 0,
	}
	count := min(len(calls), len(results))
	c.evidence.ToolExecutions += count
	for index := 0; index < count; index++ {
		scope, ok := c.classifier.Classify(calls[index])
		if !ok {
			continue
		}
		command, _ := calls[index].Arguments["command"].(string)
		c.evidence.RecordVerification(VerificationEvidence{
			ToolUseID: calls[index].ToolID, Command: command,
			Scope: scope, Passed: !results[index].IsError,
		})
		observation.VerificationAttempted = true
	}
	if !c.evidence.DiffAvailable && hasExplicitWriteCall(calls[:count]) {
		observation.WorkspaceChanged = true
	}
	return observation
}

func (c *runEvidenceCoordinator) BeforeFinalize(ctx context.Context, workspace string, softBudget bool) (phaseObservation, []QualityWarning) {
	previousRevision := c.evidence.LastChangeRevision
	c.refresh(ctx, workspace)
	c.evidence.SoftBudgetHit = c.evidence.SoftBudgetHit || softBudget
	c.evidence.FinalRequested = c.evidence.FinalRequested || !softBudget
	observation := phaseObservation{
		WorkspaceChanged: c.evidence.LastChangeRevision > previousRevision && len(c.evidence.Changes) > 0,
		FinalRequested:   !softBudget,
		SoftBudgetHit:    softBudget,
	}
	return observation, c.gate.Evaluate(*c.evidence)
}

func (c *runEvidenceCoordinator) Evidence() RunEvidence {
	copy := *c.evidence
	copy.Changes = cloneWorkspaceChanges(c.evidence.Changes)
	copy.Verifications = append([]VerificationEvidence(nil), c.evidence.Verifications...)
	return copy
}

func (c *runEvidenceCoordinator) refresh(ctx context.Context, workspace string) {
	if !c.baselineLoaded {
		c.evidence.DiffAvailable = false
		return
	}
	current, err := c.detector.Snapshot(ctx, workspace)
	if err != nil {
		c.evidence.DiffAvailable = false
		return
	}
	report, err := c.detector.Compare(c.baseline, current)
	if err != nil {
		c.evidence.DiffAvailable = false
		return
	}
	c.evidence.DiffAvailable = c.evidence.DiffAvailable && report.Complete
	c.evidence.RecordChanges(report.Changes)
}

func hasExplicitWriteCall(calls []llm.ToolCallComplete) bool {
	for _, call := range calls {
		if isWriteTool(call.ToolName) {
			return true
		}
	}
	return false
}
