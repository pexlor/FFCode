package agent

import (
	"strings"
)

type RunPhase string

const (
	PhaseExplore   RunPhase = "explore"
	PhaseImplement RunPhase = "implement"
	PhaseVerify    RunPhase = "verify"
	PhaseFinalize  RunPhase = "finalize"
)

type PhaseReason string

const (
	PhaseReasonRunStarted            PhaseReason = "run_started"
	PhaseReasonWriteTool             PhaseReason = "write_tool"
	PhaseReasonWorkspaceChanged      PhaseReason = "workspace_changed"
	PhaseReasonVerificationAttempted PhaseReason = "verification_attempted"
	PhaseReasonFinalRequested        PhaseReason = "final_requested"
	PhaseReasonSoftBudget            PhaseReason = "soft_budget"
	PhaseReasonNoProgress            PhaseReason = "no_progress"
)

const softBudgetRatio = 0.75

type phaseTransition struct {
	From    RunPhase
	To      RunPhase
	Reason  PhaseReason
	Changed bool
}

type runPhaseController struct{ current RunPhase }

type phaseObservation struct {
	WorkspaceChanged      bool
	VerificationAttempted bool
	FinalRequested        bool
	SoftBudgetHit         bool
}

func newRunPhaseController() *runPhaseController {
	return &runPhaseController{current: PhaseExplore}
}

func (c *runPhaseController) phase() RunPhase { return c.current }

func (c *runPhaseController) transition(to RunPhase, reason PhaseReason) phaseTransition {
	from := c.current
	if from == PhaseFinalize || from == to {
		return phaseTransition{From: from, To: from}
	}
	c.current = to
	return phaseTransition{From: from, To: to, Reason: reason, Changed: true}
}

func (c *runPhaseController) observeBudget(snapshot runBudgetSnapshot) phaseTransition {
	if snapshot.softLimitReached(softBudgetRatio) {
		return c.observe(phaseObservation{SoftBudgetHit: true})
	}
	return phaseTransition{From: c.current, To: c.current}
}

func (c *runPhaseController) observe(observation phaseObservation) phaseTransition {
	switch {
	case observation.SoftBudgetHit:
		return c.transition(PhaseFinalize, PhaseReasonSoftBudget)
	case observation.FinalRequested:
		return c.transition(PhaseFinalize, PhaseReasonFinalRequested)
	case observation.WorkspaceChanged:
		return c.transition(PhaseImplement, PhaseReasonWorkspaceChanged)
	case observation.VerificationAttempted && c.current != PhaseExplore:
		return c.transition(PhaseVerify, PhaseReasonVerificationAttempted)
	default:
		return phaseTransition{From: c.current, To: c.current}
	}
}

func runPhaseGuidance(phase RunPhase) string {
	switch phase {
	case PhaseExplore:
		return "# Run phase\nYou are in the exploration phase. Gather only the evidence needed to choose a concrete implementation."
	case PhaseImplement:
		return "# Run phase\nYou are in the implementation phase. Make the smallest coherent change and avoid expanding the task scope."
	case PhaseVerify:
		return "# Run phase\nYou are in the verification phase. Run focused checks and fix only failures caused by the current change."
	case PhaseFinalize:
		return "# Run phase\nYou are in the finalization phase. Stop broad exploration, preserve useful changes, run only essential checks, and conclude."
	}
	return ""
}

func isWriteTool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "writefile", "editfile", "apply_patch", "write_file", "edit_file":
		return true
	default:
		return false
	}
}
