package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

const noPatchToolWarningThreshold = 20

type WarningSeverity string

const WarningSeverityWarning WarningSeverity = "warning"

type QualityWarning struct {
	Code     string
	Severity WarningSeverity
	Message  string
	Evidence []string
}

type qualityGate struct {
	emitted map[string]struct{}
}

func newQualityGate() *qualityGate {
	return &qualityGate{emitted: make(map[string]struct{})}
}

func (g *qualityGate) Evaluate(evidence RunEvidence) []QualityWarning {
	candidates := qualityWarningCandidates(evidence)
	warnings := make([]QualityWarning, 0, len(candidates))
	for _, warning := range candidates {
		warning.Evidence = append([]string(nil), warning.Evidence...)
		sort.Strings(warning.Evidence)
		key := qualityWarningKey(warning)
		if _, exists := g.emitted[key]; exists {
			continue
		}
		g.emitted[key] = struct{}{}
		warnings = append(warnings, warning)
	}
	return warnings
}

func qualityWarningCandidates(evidence RunEvidence) []QualityWarning {
	sourcePaths := changePaths(evidence.Changes, func(change WorkspaceChange) bool { return change.Kind == ChangeSource })
	testPaths := changePaths(evidence.Changes, func(change WorkspaceChange) bool { return change.Kind == ChangeTest })
	riskyTestPaths := changePaths(evidence.Changes, func(change WorkspaceChange) bool {
		return change.Kind == ChangeTest && (change.Operation == ChangeDeleted || change.TestExpectationChanged)
	})
	postPatch := postPatchVerifications(evidence.Verifications)

	var warnings []QualityWarning
	if len(sourcePaths) > 0 && len(postPatch) == 0 {
		warnings = append(warnings, newQualityWarning("QG001", "source changes were not verified after the patch", sourcePaths))
	}
	if len(postPatch) > 0 && !postPatch[len(postPatch)-1].Passed {
		warnings = append(warnings, newQualityWarning("QG002", "the latest post-patch verification failed", verificationEvidence(postPatch[len(postPatch)-1])))
	}
	if len(testPaths) > 0 && len(sourcePaths) == 0 {
		warnings = append(warnings, newQualityWarning("QG003", "tests changed without source changes", testPaths))
	}
	if len(riskyTestPaths) > 0 {
		warnings = append(warnings, newQualityWarning("QG004", "existing test expectations or deleted tests require review", riskyTestPaths))
	}
	if fallbackOnly(postPatch) {
		warnings = append(warnings, newQualityWarning("QG005", "post-patch verification used fallback checks only", verificationEvidence(postPatch...)))
	}
	if len(postPatch) > 0 && evidence.LastChangeRevision > evidence.LastVerificationRevision {
		warnings = append(warnings, newQualityWarning("QG006", "workspace changed after the most recent verification", allChangePaths(evidence.Changes)))
	}
	if !evidence.DiffAvailable {
		warnings = append(warnings, newQualityWarning("QG007", "workspace diff evidence is unavailable or incomplete", nil))
	}
	if len(evidence.Changes) == 0 && evidence.ToolExecutions >= noPatchToolWarningThreshold {
		warnings = append(warnings, newQualityWarning("QG008", "the run ended without a patch after substantial tool activity", nil))
	}
	return warnings
}

func newQualityWarning(code, message string, evidence []string) QualityWarning {
	return QualityWarning{Code: code, Severity: WarningSeverityWarning, Message: message, Evidence: evidence}
}

func changePaths(changes []WorkspaceChange, include func(WorkspaceChange) bool) []string {
	paths := make([]string, 0, len(changes))
	for _, change := range changes {
		if include(change) {
			paths = append(paths, change.Path)
		}
	}
	return paths
}

func allChangePaths(changes []WorkspaceChange) []string {
	return changePaths(changes, func(WorkspaceChange) bool { return true })
}

func postPatchVerifications(verifications []VerificationEvidence) []VerificationEvidence {
	result := make([]VerificationEvidence, 0, len(verifications))
	for _, verification := range verifications {
		if verification.AfterPatch {
			result = append(result, verification)
		}
	}
	return result
}

func fallbackOnly(verifications []VerificationEvidence) bool {
	if len(verifications) == 0 {
		return false
	}
	for _, verification := range verifications {
		if verification.Scope != VerificationFallback {
			return false
		}
	}
	return true
}

func verificationEvidence(verifications ...VerificationEvidence) []string {
	values := make([]string, 0, len(verifications))
	for _, verification := range verifications {
		value := strings.TrimSpace(verification.Command)
		if value == "" {
			value = verification.ToolUseID
		}
		if value != "" {
			values = append(values, value)
		}
	}
	return values
}

func qualityWarningKey(warning QualityWarning) string {
	digest := sha256.New()
	digest.Write([]byte(warning.Code))
	for _, value := range warning.Evidence {
		digest.Write([]byte{0})
		digest.Write([]byte(value))
	}
	return hex.EncodeToString(digest.Sum(nil))
}
