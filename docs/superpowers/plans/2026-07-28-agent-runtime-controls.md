# Agent Runtime Controls Implementation Plan

> **For AI workers:** Use `executing-plans` and `test-driven-development` to implement each task independently. Every task ends in its own commit.

**Goal:** Make a single FFCode run bounded, convergent, retryable, safely scheduled, and recoverable after cancellation or timeout.

**Architecture:** Add small control components around the existing Agent loop instead of moving session, provider, or tool ownership into `agent`. Runtime policy remains configurable per run; provider retry stays in `llm`; tool access classification and scheduling stay in `tool`; checkpoints use a dedicated store interface.

**Technical stack:** Go 1.25, standard library, existing Agent events and file-backed conversation storage.

---

### Task 1: Run budget

**Files:**
- Create: `internal/agent/run_budget.go`
- Create: `internal/agent/run_budget_test.go`
- Modify: `internal/agent/agent.go`, `internal/agent/events.go`

- [ ] Write tests proving duration, cumulative token, and tool-call limits terminate a run with `budget_exhausted` or `deadline_exceeded`.
- [ ] Run `go test ./internal/agent` and confirm the new tests fail for missing APIs.
- [ ] Add `RunBudget`, a per-run usage ledger, and `RunContextWithBudget` while preserving existing `Run` and `RunContext` call sites.
- [ ] Run `gofmt`, `go test ./...`, and `go test -race ./internal/agent`.
- [ ] Commit as `feat(agent): add run resource budgets`.

### Task 2: Run phases

**Files:**
- Create: `internal/agent/run_phase.go`
- Create: `internal/agent/run_phase_test.go`
- Modify: `internal/agent/agent.go`, `internal/agent/events.go`

- [ ] Write tests for phase transitions caused by soft time/token thresholds and tool activity.
- [ ] Run the focused tests and confirm they fail.
- [ ] Add explore, implement, verify, and finalize phases with bounded phase guidance appended to model context.
- [ ] Ensure a soft budget threshold enters finalize before the hard budget terminates the run.
- [ ] Run formatting, all tests, and the Agent race suite.
- [ ] Commit as `feat(agent): add run phase control`.

### Task 3: Provider retries

**Files:**
- Create: `internal/llm/provider_error.go`
- Create: `internal/llm/provider_error_test.go`
- Modify: `internal/llm/anthropic.go`, `internal/llm/openai.go`, `internal/agent/agent.go`
- Add or modify provider stream tests as needed.

- [ ] Write tests for retry classification, `Retry-After`, exponential backoff, retry exhaustion, and cancellation.
- [ ] Confirm focused tests fail.
- [ ] Normalize HTTP and transient transport errors as `ProviderError` and add a bounded retry policy with injectable sleeping.
- [ ] Buffer each attempt until success so a failed stream cannot emit duplicate text or tool calls.
- [ ] Run formatting, all tests, and Agent/LLM race suites.
- [ ] Commit as `feat(llm): retry transient provider failures`.

### Task 4: Loop and no-progress detection

**Files:**
- Create: `internal/agent/progress.go`
- Create: `internal/agent/progress_test.go`
- Modify: `internal/agent/agent.go`, `internal/agent/events.go`, `internal/protocol/encoder.go`

- [ ] Write tests for repeated call/result fingerprints, warning, blocking, and no-progress termination.
- [ ] Confirm focused tests fail.
- [ ] Record canonical tool arguments, result hashes, and an inexpensive workspace fingerprint.
- [ ] Emit machine-readable progress events, inject bounded convergence guidance, and terminate persistent no-progress runs.
- [ ] Run formatting, all tests, and Agent/protocol race suites.
- [ ] Commit as `feat(agent): detect repeated no-progress loops`.

### Task 5: Tool scheduling

**Files:**
- Create: `internal/tool/scheduler.go`
- Create: `internal/tool/scheduler_test.go`
- Modify: `internal/tool/tool.go`, `internal/tool/registry.go`, built-in tool declarations, `internal/agent/agent.go`

- [ ] Write timing/order tests proving adjacent reads overlap while writes and unknown tools serialize.
- [ ] Confirm focused tests fail.
- [ ] Add explicit read/write/exclusive access metadata and a scheduler that preserves result order.
- [ ] Move batch execution out of Agent and preserve cancellation-safe tool IDs.
- [ ] Run formatting, all tests, and Agent/tool race suites.
- [ ] Commit as `feat(tool): schedule reads concurrently and writes serially`.

### Task 6: Checkpoint and recovery

**Files:**
- Create: `internal/agent/checkpoint.go`
- Create: `internal/agent/checkpoint_test.go`
- Create: `internal/storage/filecheckpoint/store.go`
- Create: `internal/storage/filecheckpoint/store_test.go`
- Modify: `internal/agent/agent.go`, `internal/app/bootstrap.go`

- [ ] Write tests for atomic generations, cancellation save, workspace mismatch, completed-call preservation, and recovery guidance.
- [ ] Confirm focused tests fail.
- [ ] Add a checkpoint store interface and file implementation with atomic generation writes.
- [ ] Save after committed model/tool boundaries and on cancellation or timeout; load and reconcile before a resumed run.
- [ ] Never replay completed side-effecting calls automatically.
- [ ] Run formatting, all tests, and Agent/storage race suites.
- [ ] Commit as `feat(agent): checkpoint and recover interrupted runs`.

