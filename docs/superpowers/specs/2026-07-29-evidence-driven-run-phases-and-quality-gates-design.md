# Evidence-Driven Run Phases and Advisory Quality Gates Design

## Status

Approved for implementation planning on 2026-07-29.

## Background

FFCode currently infers run phases from requested tool names and a short list of
test command substrings. This creates two observable errors in SWE-bench runs:

- writes performed through `Bash`, including `apply_patch`, do not enter the
  implement phase;
- project-specific test commands such as Django's `tests/runtests.py` do not
  enter the verify phase.

The phase controller can also treat one successful recognized command as
sufficient evidence to finalize even when code changed afterward or the command
was only a fallback check. As a result, phase events do not reliably describe
the work performed and cannot support meaningful quality checks.

This design replaces tool-name-driven phase inference with evidence gathered
from the workspace and tool outcomes. It also introduces advisory quality gates.
Warnings are visible to users and machine-readable consumers, but they never
block completion or request additional confirmation in the first version.

## Goals

1. Make run phases describe observed work rather than the tool syntax used to
   perform it.
2. Distinguish verification activity from successful, relevant verification.
3. Warn when the evidence does not support a high-confidence completion claim.
4. Preserve current terminal behavior and JSONL protocol compatibility.
5. Keep evidence collection, phase decisions, and quality policy independently
   testable.
6. Provide structured process metrics for the existing SWE-bench failure set.

## Non-Goals

- Blocking completion or requiring user approval after a warning.
- Automatically starting an additional reviewer or model turn.
- Proving semantic correctness of a patch.
- Adding a user-configurable rule language.
- Replacing the existing run budget, progress tracker, checkpoint, or tool
  scheduler.
- Supporting agent teams.

## Alternatives Considered

### Extend the existing string rules

Adding more write-tool names and test substrings would be a small change, but it
would continue to couple workflow state to incidental command syntax. It cannot
reliably handle shell writes, initial dirty workspaces, or changes made after a
successful test.

### Evidence-driven policy engine

The selected design records workspace and tool facts, derives phases from those
facts, and evaluates warnings separately. It is incremental, testable, and does
not require changing the surrounding conversation or context architecture.

### Full workflow engine

A persisted workflow graph could eventually support resumable plans and strict
gates. It would also change session, checkpoint, context, and event ownership at
once. That scope is not justified by the current failure data.

## Architecture

The design has three independent components:

```text
model response -> tool batch -> EvidenceCollector -> RunEvidence
                                              |-> PhaseDecider -> RunPhaseEvent
                                              |-> QualityGate -> QualityWarningEvent
```

`EvidenceCollector` records facts. It does not assign phases or severity.

`PhaseDecider` is a deterministic state machine over evidence observations. It
does not inspect raw shell commands or emit UI events directly.

`QualityGate` evaluates accumulated evidence at defined checkpoints and returns
deduplicated advisory warnings. It does not alter control flow.

The Agent remains the orchestrator: it chooses observation points, passes facts
to the two pure policy components, and publishes their results.

## Evidence Model

The core model lives in `internal/agent/evidence.go`:

```go
type RunEvidence struct {
	Baseline       WorkspaceSnapshot
	Current        WorkspaceSnapshot
	Changes        []WorkspaceChange
	Verifications  []VerificationEvidence
	ToolExecutions []ToolExecutionEvidence
	FinalRequested bool
	SoftBudgetHit  bool
	DiffAvailable  bool
}

type WorkspaceChange struct {
	Path      string
	Kind      ChangeKind
	Operation ChangeOperation
}

type VerificationEvidence struct {
	ToolUseID  string
	Command    string
	Scope      VerificationScope
	Passed     bool
	AfterPatch bool
}
```

`ChangeKind` has `source`, `test`, `docs`, `config`, and `unknown` values.
Classification is path-based and deliberately conservative. An unrecognized
path is `unknown`, never silently treated as documentation or test code.

`VerificationScope` has `focused`, `package`, `full`, `fallback`, and `unknown`
values. `git diff --check`, syntax compilation, and direct function invocations
are fallback evidence and do not count as a complete test run.

Evidence records bounded metadata only. Unified diff content may be inspected
while computing test-change warnings but is not retained in `RunEvidence` or
emitted through events.

## Workspace Baseline and Change Detection

A `ChangeDetector` interface isolates Git interaction:

```go
type ChangeDetector interface {
	Snapshot(context.Context, string) (WorkspaceSnapshot, error)
	Compare(WorkspaceSnapshot, WorkspaceSnapshot) ([]WorkspaceChange, error)
}
```

The default Git implementation captures the state at run start and compares
later snapshots against that baseline. Pre-existing staged, unstaged, and
untracked changes are therefore not attributed to the current run unless their
content changes during the run.

Snapshots contain hashes and bounded file metadata, not full repository copies.
The implementation may use Git status and diff output internally. Diff reads
must have a fixed byte limit. Exceeding the limit marks diff evidence as
unavailable and produces `QG007`; it must not fail the run.

Observation happens only at these boundaries:

1. once after run validation and before the first model request;
2. after each executed tool batch;
3. before accepting a model response with no tool calls;
4. when the soft budget first requests finalization.

Read-only batches can skip the post-batch snapshot when every executed tool is
registered as read access. Unknown, write, and exclusive tools require a
snapshot because their actual side effects cannot be inferred safely.

When the workspace is not a Git repository or Git inspection fails, the run
continues. `DiffAvailable` becomes false, phase decisions fall back to explicit
tool metadata, and `QG007` explains the reduced confidence. No recursive file
snapshot fallback is included in the first version.

## Verification Classification

Verification recognition moves to `internal/agent/verification.go` behind this
interface:

```go
type VerificationClassifier interface {
	Classify(llm.ToolCallComplete) (VerificationScope, bool)
}
```

The built-in classifier recognizes at least:

- `go test` and `go vet`;
- `pytest` and `python -m pytest`;
- Python project runners ending in `tests/runtests.py`;
- `npm test`, `pnpm test`, and `yarn test`;
- `cargo test`;
- `make test` and `just test`;
- fallback checks including `git diff --check`, syntax-only compilation, and
  direct one-off scripts.

Classification is a hint derived from a tool call. Pass or failure comes from
the corresponding `ToolResult`. A failed command still proves that verification
was attempted and moves an implemented run into the verify phase.

Commands before the first run-attributed workspace change have
`AfterPatch=false`. They establish a baseline but cannot satisfy post-change
verification gates.

## Phase State Machine

The phase controller consumes observations instead of raw tool calls:

| Current phase | Observation | Next phase |
| --- | --- | --- |
| Explore | run-attributed workspace change | Implement |
| Explore | verification without a change | Explore |
| Implement | verification attempt after a change | Verify |
| Verify | newer workspace change | Implement |
| Verify | model requests completion | Finalize |
| Any non-final phase | soft budget reached | Finalize |
| Any non-final phase | model requests completion | Finalize |

The state machine is allowed to move from Verify back to Implement. Finalize is
terminal for the current run, matching existing behavior.

Finalize means "the run is ending", not "verification succeeded". Verification
quality is represented by evidence and warnings, not overloaded into phase
state.

When diff evidence is unavailable, explicit registered write tools may still
move Explore to Implement. Shell execution remains exclusive and does not count
as a write by itself; this intentionally produces `QG007` instead of claiming
precise workspace knowledge.

## Advisory Quality Gates

Quality gates run before a normal terminal completion and when soft-budget
finalization is first entered. Warnings are deduplicated by code and evidence
identity so the terminal UI does not repeat unchanged messages.

The first version defines:

| Code | Condition |
| --- | --- |
| `QG001` | Source changed with no post-change verification attempt |
| `QG002` | The latest relevant post-change verification failed |
| `QG003` | The run changed tests but no source files |
| `QG004` | Existing test assertions were modified or tests were deleted |
| `QG005` | Post-change verification consists only of fallback checks |
| `QG006` | Workspace changed after the most recent verification |
| `QG007` | Workspace diff evidence is unavailable or exceeded its limit |
| `QG008` | The run consumed substantial exploration effort and ended without a patch |

`QG004` is intentionally heuristic. It inspects only run-attributed diff hunks
under test-classified paths for removed assertion or expectation lines, and for
deleted test files. Its message must say that review is required, not that the
change is necessarily wrong.

`QG008` uses existing progress information rather than introducing another time
budget. It fires when the run performed at least the configured no-patch tool
threshold and has no run-attributed workspace change. The initial threshold is
20 executed tool calls and remains an internal policy constant in version one.

Warnings never:

- change `TurnStatus` or `StopReason`;
- prevent checkpoint persistence;
- trigger another model request;
- request permission or confirmation;
- cause a non-zero CLI exit code.

## Events and Protocol Compatibility

The Agent adds a UI-neutral event:

```go
type QualityWarningEvent struct {
	Code     string
	Severity WarningSeverity
	Message  string
	Evidence []string
}
```

The first version supports only the `warning` severity value. Keeping the field
allows future renderers to distinguish informational and blocking policies
without changing the event shape.

JSONL encodes the event as `quality_warning`:

```json
{
  "version": 1,
  "type": "quality_warning",
  "data": {
    "code": "QG001",
    "severity": "warning",
    "message": "source changes were not verified after the patch",
    "evidence": ["internal/agent/run_phase.go"]
  }
}
```

Protocol version remains 1 because the stream is extensible and existing
consumers already ignore event types they do not understand. `TurnEndEvent` is
unchanged and remains the only terminal signal. The quality warning must be
emitted before the terminal event.

The terminal renderer shows warnings after active tool output and before the
final completion status. It uses a stable `Quality warning <code>:` prefix and
does not render warnings as execution failures.

## Agent Integration

`internal/agent/agent.go` gains one run-local coordinator that owns the
collector, phase controller, and gate evaluator. The main loop calls small
methods at observation boundaries rather than implementing policy inline.

The no-tool-call path changes ordering to:

1. record that the model requested completion;
2. refresh workspace evidence;
3. transition to Finalize and emit the phase event if needed;
4. evaluate and emit new warnings;
5. append the assistant response and synchronize session context;
6. save the completed checkpoint;
7. emit the existing terminal event.

Warnings are runtime diagnostics and are not appended to conversation history.
They must not consume model context or influence future resumed sessions.

The progress tracker remains responsible for repeated identical calls and lack
of forward motion. The quality gate reads its aggregate tool count for `QG008`
but does not duplicate its blocking or finalization behavior.

## Checkpoint and Recovery

Evidence is run-local in the first version and is not added to checkpoint or
session storage. A resumed interrupted run establishes a new baseline after
checkpoint reconciliation. Existing workspace-change recovery guidance remains
responsible for warning that files changed while the run was interrupted.

Persisting evidence can be considered later if benchmark data shows that a new
baseline after recovery hides meaningful verification gaps. Avoiding a storage
format change keeps this feature independent of checkpoint compatibility.

## Failure Handling

- Baseline failure: continue with fallback phase inference and emit `QG007` at
  finalization.
- Later snapshot failure: retain the last valid snapshot, mark evidence
  incomplete, and emit `QG007`.
- Verification classification failure: record the execution as unknown; never
  convert it into a passed verification.
- Quality rule panic or unexpected error: recover at the coordinator boundary,
  emit one diagnostic warning when possible, and allow the normal terminal path
  to continue.
- Event renderer incompatibility: unknown events are ignored according to the
  existing protocol contract.

## File Ownership

Create:

- `internal/agent/evidence.go`: evidence types and run-local collection.
- `internal/agent/change_detector.go`: bounded Git snapshots and comparison.
- `internal/agent/verification.go`: command classification and scopes.
- `internal/agent/quality_gate.go`: advisory rule evaluation and deduplication.
- focused test files matching each new source file.

Modify:

- `internal/agent/run_phase.go`: accept evidence observations and remove direct
  command-string classification.
- `internal/agent/agent.go`: call the run coordinator at observation points.
- `internal/agent/events.go`: define `QualityWarningEvent`.
- `internal/protocol/event.go` and encoder: encode `quality_warning`.
- `internal/ui/terminal`: render advisory warnings.
- `internal/ui/jsonl`: verify event ordering and compatibility.
- Agent and protocol READMEs: document phase semantics and the new event.

## Testing Strategy

Unit tests cover:

- a dirty initial workspace does not count as a run change;
- a shell-applied patch enters Implement when Git evidence is available;
- baseline tests do not satisfy post-patch verification;
- Django `tests/runtests.py` enters Verify;
- failed verification enters Verify and produces `QG002`;
- a code edit after passing tests returns Verify to Implement and produces
  `QG006` at completion;
- fallback-only checks produce `QG005`;
- test-only changes produce `QG003`;
- assertion changes produce one deduplicated `QG004` warning;
- unavailable and oversized diffs produce `QG007` without failing the run;
- warnings precede `TurnEndEvent` and do not change its fields;
- unknown JSONL consumers can ignore the new event.

Integration tests use temporary Git repositories and real file changes. Pure
phase and quality rules use in-memory evidence and table-driven tests.

Because Agent execution, workspace observation, and tool scheduling are
concurrent, implementation verification must include:

```bash
gofmt -w <changed-go-files>
go test ./...
go test -race ./internal/agent/... ./internal/tool/... ./internal/ui/...
go vet ./...
```

## Rollout and Measurement

The feature is always enabled because it changes diagnostics rather than tool
authorization or completion behavior. No feature flag is needed.

Before rerunning the full benchmark, replay the existing 26 unresolved and two
empty-patch cases. Record:

- phase classification accuracy from logs;
- warning counts by code;
- false-positive `QG004` rate;
- source patches without post-change verification;
- verification followed by unverified edits;
- empty patches after at least 20 tool calls;
- resolved count and PASS_TO_PASS regressions.

The first rollout is accepted when:

1. Bash-applied patches and Django test runners are classified correctly in
   targeted regression logs;
2. no warning changes terminal status or runner completion detection;
3. all repository tests and required race tests pass;
4. manual review of the 28-case replay finds no more than 10% false positives
   among `QG001` through `QG007` warnings;
5. every warning can cite concrete file or command evidence.

Improving SWE-bench resolved rate is a downstream objective, not a release gate
for this observability-focused first version.

## Future Extensions

After warning precision is measured, the same policy interface can support an
optional strict mode, an automatic reviewer turn, project-specific verification
classifiers, or persisted evidence. None of these extensions are required by
this design.
