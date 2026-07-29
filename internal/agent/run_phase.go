package agent

import (
	"strings"

	"MyCode/internal/llm"
	"MyCode/internal/tool"
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
	PhaseReasonRunStarted           PhaseReason = "run_started"
	PhaseReasonWriteTool            PhaseReason = "write_tool"
	PhaseReasonVerificationTool     PhaseReason = "verification_tool"
	PhaseReasonVerificationComplete PhaseReason = "verification_complete"
	PhaseReasonSoftBudget           PhaseReason = "soft_budget"
	PhaseReasonNoProgress           PhaseReason = "no_progress"
)

const softBudgetRatio = 0.75

type phaseTransition struct {
	From    RunPhase
	To      RunPhase
	Reason  PhaseReason
	Changed bool
}

type runPhaseController struct{ current RunPhase }

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
		return c.transition(PhaseFinalize, PhaseReasonSoftBudget)
	}
	return phaseTransition{From: c.current, To: c.current}
}

func (c *runPhaseController) observeToolCalls(calls []llm.ToolCallComplete) phaseTransition {
	if c.current == PhaseFinalize {
		return phaseTransition{From: c.current, To: c.current}
	}
	for _, call := range calls {
		if isVerificationCall(call) && c.current != PhaseExplore {
			return c.transition(PhaseVerify, PhaseReasonVerificationTool)
		}
	}
	for _, call := range calls {
		if isWriteTool(call.ToolName) {
			return c.transition(PhaseImplement, PhaseReasonWriteTool)
		}
	}
	return phaseTransition{From: c.current, To: c.current}
}

func (c *runPhaseController) observeToolResults(calls []llm.ToolCallComplete, results []tool.ToolResult) phaseTransition {
	if c.current != PhaseVerify || len(calls) != len(results) {
		return phaseTransition{From: c.current, To: c.current}
	}
	foundVerification := false
	for index, call := range calls {
		if !isVerificationCall(call) {
			continue
		}
		foundVerification = true
		if results[index].IsError {
			return phaseTransition{From: c.current, To: c.current}
		}
	}
	if foundVerification {
		return c.transition(PhaseFinalize, PhaseReasonVerificationComplete)
	}
	return phaseTransition{From: c.current, To: c.current}
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

func isVerificationCall(call llm.ToolCallComplete) bool {
	_, ok := (defaultVerificationClassifier{}).Classify(call)
	return ok
}
