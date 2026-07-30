package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"FFCode/internal/llm"
	"FFCode/internal/tool"
)

const maxProgressGuidanceBytes = 640

type ProgressKind string

const (
	ProgressNone        ProgressKind = "none"
	ProgressWarning     ProgressKind = "warning"
	ProgressToolBlocked ProgressKind = "tool_blocked"
	ProgressFinalize    ProgressKind = "finalize"
	ProgressStop        ProgressKind = "stop"
)

type ProgressPolicy struct {
	WarnRepeat    int
	BlockRepeat   int
	FinalizeAfter int
	StopAfter     int
}

func DefaultProgressPolicy() ProgressPolicy {
	return ProgressPolicy{WarnRepeat: 2, BlockRepeat: 3, FinalizeAfter: 2, StopAfter: 3}
}

// ProgressFingerprinter summarizes workspace state without retaining file
// contents. Implementations should return the same value while the workspace
// is unchanged.
type ProgressFingerprinter interface {
	Fingerprint(ctx context.Context, workspace string) (string, error)
}

type gitProgressFingerprinter struct{}

func (gitProgressFingerprinter) Fingerprint(ctx context.Context, workspace string) (string, error) {
	if strings.TrimSpace(workspace) == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return "", err
		}
	}
	workspace, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}

	digest := sha256.New()
	commands := [][]string{
		{"status", "--porcelain=v1", "-z", "--untracked-files=all"},
		{"diff", "--no-ext-diff", "--binary"},
		{"diff", "--no-ext-diff", "--binary", "--cached"},
	}
	for _, arguments := range commands {
		if err := streamGitCommand(ctx, workspace, digest, arguments); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func streamGitCommand(ctx context.Context, workspace string, destination hash.Hash, arguments []string) error {
	if _, err := io.WriteString(destination, strings.Join(arguments, "\x00")+"\x00"); err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "git", append([]string{"-C", workspace}, arguments...)...)
	command.Stdout = destination
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return fmt.Errorf("fingerprint workspace with git %s: %w", arguments[0], err)
	}
	return nil
}

type ProgressDecision struct {
	Kind       ProgressKind
	Repetition int
	ToolUseID  string
	Message    string
}

type progressObservation struct {
	result    string
	workspace string
	repeats   int
}

type progressTracker struct {
	policy              ProgressPolicy
	observations        map[string]progressObservation
	noProgress          int
	guidance            string
	finalizeInstruction bool
}

func newProgressTracker(policy ProgressPolicy) *progressTracker {
	return &progressTracker{policy: normalizeProgressPolicy(policy), observations: make(map[string]progressObservation)}
}

func normalizeProgressPolicy(policy ProgressPolicy) ProgressPolicy {
	defaults := DefaultProgressPolicy()
	if policy.WarnRepeat <= 0 {
		policy.WarnRepeat = defaults.WarnRepeat
	}
	if policy.BlockRepeat <= policy.WarnRepeat {
		policy.BlockRepeat = max(defaults.BlockRepeat, policy.WarnRepeat+1)
	}
	if policy.FinalizeAfter <= 0 {
		policy.FinalizeAfter = defaults.FinalizeAfter
	}
	if policy.StopAfter <= policy.FinalizeAfter {
		policy.StopAfter = max(defaults.StopAfter, policy.FinalizeAfter+1)
	}
	return policy
}

func (t *progressTracker) beforeTools(calls []llm.ToolCallComplete, workspace string) []ProgressDecision {
	var decisions []ProgressDecision
	for _, call := range calls {
		observation, ok := t.observations[toolCallFingerprint(call)]
		if !ok || observation.workspace != workspace || observation.repeats+1 < t.policy.BlockRepeat {
			continue
		}
		decisions = append(decisions, ProgressDecision{
			Kind:       ProgressToolBlocked,
			Repetition: observation.repeats + 1,
			ToolUseID:  call.ToolID,
			Message:    "blocked repeated tool call because neither its result nor the workspace changed",
		})
	}
	return decisions
}

func (t *progressTracker) observe(calls []llm.ToolCallComplete, results []tool.ToolResult, workspace string) ProgressDecision {
	progressed := false
	decision := ProgressDecision{Kind: ProgressNone}
	for index, call := range calls {
		if index >= len(results) {
			break
		}
		callHash := toolCallFingerprint(call)
		resultHash := toolResultFingerprint(results[index])
		previous, ok := t.observations[callHash]
		repeats := 1
		if ok && previous.workspace == workspace && previous.result == resultHash {
			repeats = previous.repeats + 1
		} else {
			progressed = true
		}
		t.observations[callHash] = progressObservation{result: resultHash, workspace: workspace, repeats: repeats}
		if repeats >= t.policy.WarnRepeat && repeats < t.policy.BlockRepeat && repeats > decision.Repetition {
			decision = ProgressDecision{
				Kind:       ProgressWarning,
				Repetition: repeats,
				ToolUseID:  call.ToolID,
				Message:    "repeated tool call produced the same result without changing the workspace",
			}
		}
	}
	if progressed {
		t.noProgress = 0
		t.guidance = ""
		t.finalizeInstruction = false
	} else if decision.Kind == ProgressWarning {
		t.noProgress++
		t.setGuidance("The previous tool call repeated without measurable progress. Change approach or arguments; do not repeat the same call unchanged.")
	}
	return decision
}

func (t *progressTracker) recordBlocked() ProgressDecision {
	t.noProgress++
	switch {
	case t.noProgress >= t.policy.StopAfter:
		t.setGuidance("Stop using tools and return the best concise final response now, clearly stating any unresolved limitation.")
		return ProgressDecision{Kind: ProgressStop, Repetition: t.noProgress, Message: "stopping after sustained lack of progress"}
	case t.noProgress >= t.policy.FinalizeAfter && !t.finalizeInstruction:
		t.finalizeInstruction = true
		t.setGuidance("No further tool retries are allowed. Finalize now with the best supported answer and state any unresolved limitation.")
		return ProgressDecision{Kind: ProgressFinalize, Repetition: t.noProgress, Message: "forcing finalization after repeated lack of progress"}
	default:
		return ProgressDecision{Kind: ProgressNone, Repetition: t.noProgress}
	}
}

func (t *progressTracker) convergenceGuidance() string {
	return t.guidance
}

func (t *progressTracker) setGuidance(guidance string) {
	if len(guidance) > maxProgressGuidanceBytes {
		guidance = guidance[:maxProgressGuidanceBytes]
	}
	t.guidance = guidance
}

func toolCallFingerprint(call llm.ToolCallComplete) string {
	arguments, err := json.Marshal(call.Arguments)
	if err != nil {
		arguments = []byte("invalid-json-arguments")
	}
	return hashParts(call.ToolName, string(arguments))
}

func toolResultFingerprint(result tool.ToolResult) string {
	return hashParts(result.Output, fmt.Sprintf("%t", result.IsError))
}

func hashParts(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = io.WriteString(digest, fmt.Sprintf("%d:", len(part)))
		_, _ = io.WriteString(digest, part)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func workspaceFingerprint(ctx context.Context, fingerprinter ProgressFingerprinter, workspace string) string {
	value, err := fingerprinter.Fingerprint(ctx, workspace)
	if err == nil {
		return value
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "context-cancelled"
	}
	return hashParts("workspace-fingerprint-unavailable", workspace)
}
