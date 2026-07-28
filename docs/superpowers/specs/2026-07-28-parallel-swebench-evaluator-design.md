# Parallel SWE-bench Evaluator Design

## Goal

Increase live SWE-bench evaluation throughput while preserving per-case evaluation,
atomic progress persistence, and restartability. The active benchmark will run five
evaluations concurrently on the configured 8-CPU, 8-GB Docker runtime.

## Interface

Add `--workers` to `watch_evaluator.py`. Its default remains `1` so existing callers
keep serial behavior. The active benchmark passes `--workers 5`.

## Scheduling And State

The watcher owns one bounded thread pool. Each worker calls the existing
`evaluate_instance()` for one case, including its retry policy and isolated prediction,
log, report, and run ID paths.

Only the main thread mutates the evaluated-result map and writes
`evaluation-results.jsonl`. It tracks instance IDs currently in flight so polling cannot
submit the same case twice. Empty patches are classified and persisted by the main
thread without occupying a worker.

The scheduler fills available worker slots from completed Agent results, waits briefly
for completed futures, persists each completed result immediately, and then polls for
more work. On restart, existing evaluator results are loaded and never resubmitted.

## Failure Handling

An exception escaping a worker is converted into an `evaluator_error` record for that
instance instead of terminating the watcher. Existing evaluator retries remain inside
`evaluate_instance()`. Interrupting the watcher stops new submissions; active harness
processes may require normal SWE-bench container cleanup before restart.

Five concurrent Docker evaluations can create memory pressure on an 8-GB runtime.
Concurrency remains a command-line setting so it can be reduced to three without code
changes if containers are killed or evaluation latency rises sharply.

## Verification

Unit tests will verify that the scheduler:

- never exceeds the configured concurrency;
- evaluates each instance once;
- persists completed and skipped results through the main state owner;
- preserves restart behavior for already evaluated instances;
- records worker exceptions without stopping other evaluations.

After implementation, run the watcher unit tests, then restart only the evaluator tmux
session with `--workers 5`. Confirm five distinct cases are in flight, the state file
continues to grow, and no duplicate instance IDs or leftover conflicting containers are
present.
