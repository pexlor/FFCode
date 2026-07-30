# Parallel SWE-bench Evaluator Implementation Plan

> **For AI agent workers:** Required sub-skill: use executing-plans to implement this plan inline. Track progress with the checkboxes below.

**Goal:** Add configurable parallel case evaluation to the live SWE-bench watcher and restart the active run with five workers.

**Architecture:** A bounded `ThreadPoolExecutor` runs the existing per-case evaluator. A generator converts worker completions and exceptions into result records, while the caller remains the sole owner of the evaluated map and JSONL persistence.

**Tech Stack:** Python 3 standard library (`concurrent.futures`, `unittest`, `threading`), SWE-bench harness, Docker, tmux.

---

## File Structure

- Modify `benchmark/swebench-lite-20260726/watch_evaluator.py`: add the worker option and bounded concurrent evaluation helper.
- Modify `benchmark/swebench-lite-20260726/test_watch_evaluator.py`: verify concurrency, result completeness, and exception isolation.

### Task 1: Define Concurrent Evaluation Behavior

**Files:**
- Modify: `benchmark/swebench-lite-20260726/test_watch_evaluator.py`
- Test: `benchmark/swebench-lite-20260726/test_watch_evaluator.py`

- [ ] **Step 1: Add a failing bounded-concurrency test**

Add imports for `argparse` and `threading`, then add a test which calls the new helper with four items and two workers. The fake evaluator records active calls under a lock and uses a two-party barrier so both worker slots are occupied together.

```python
def test_evaluates_pending_with_bounded_concurrency(self):
    lock = threading.Lock()
    barrier = threading.Barrier(2)
    active = 0
    maximum_active = 0

    def evaluate(item, args):
        nonlocal active, maximum_active
        with lock:
            active += 1
            maximum_active = max(maximum_active, active)
        barrier.wait(timeout=2)
        with lock:
            active -= 1
        return {"instance_id": item["instance_id"], "status": "resolved"}

    args = argparse.Namespace(workers=2)
    pending = [{"instance_id": f"case-{index}"} for index in range(4)]

    results = list(watch_evaluator.evaluate_pending(pending, args, evaluate=evaluate))

    self.assertEqual(maximum_active, 2)
    self.assertEqual(
        {item["instance_id"] for item in results},
        {item["instance_id"] for item in pending},
    )
```

- [ ] **Step 2: Add a failing worker-exception test**

```python
def test_worker_exception_becomes_evaluator_error(self):
    def evaluate(item, args):
        if item["instance_id"] == "broken":
            raise RuntimeError("boom")
        return {"instance_id": item["instance_id"], "status": "resolved"}

    args = argparse.Namespace(workers=2)
    results = list(
        watch_evaluator.evaluate_pending(
            [{"instance_id": "broken"}, {"instance_id": "working"}],
            args,
            evaluate=evaluate,
        )
    )
    by_id = {item["instance_id"]: item for item in results}

    self.assertEqual(by_id["working"]["status"], "resolved")
    self.assertEqual(by_id["broken"]["status"], "evaluator_error")
    self.assertIn("RuntimeError('boom')", by_id["broken"]["error"])
```

- [ ] **Step 3: Run the focused tests and verify RED**

Run:

```bash
cd benchmark/swebench-lite-20260726
python3 -m unittest -v test_watch_evaluator.WatchEvaluatorTest.test_evaluates_pending_with_bounded_concurrency test_watch_evaluator.WatchEvaluatorTest.test_worker_exception_becomes_evaluator_error
```

Expected: both tests fail with `AttributeError` because `evaluate_pending` does not exist.

### Task 2: Implement Bounded Evaluation

**Files:**
- Modify: `benchmark/swebench-lite-20260726/watch_evaluator.py`
- Test: `benchmark/swebench-lite-20260726/test_watch_evaluator.py`

- [ ] **Step 1: Add the concurrent helper**

Import `concurrent.futures` and add:

```python
def evaluate_pending(pending, args, evaluate=evaluate_instance):
    with concurrent.futures.ThreadPoolExecutor(max_workers=args.workers) as pool:
        futures = {pool.submit(evaluate, item, args): item for item in pending}
        for future in concurrent.futures.as_completed(futures):
            item = futures[future]
            try:
                yield future.result()
            except Exception as exc:
                yield {
                    "instance_id": item["instance_id"],
                    "status": "evaluator_error",
                    "resolved": False,
                    "agent_status": item.get("status"),
                    "patch_bytes": item.get("patch_bytes", 0),
                    "error": repr(exc),
                }
```

- [ ] **Step 2: Add the CLI option and use the helper**

Add `parser.add_argument("--workers", type=int, default=1)` and reject values below one with `parser.error`. Replace the serial `for item in pending` evaluator call with:

```python
for item in pending:
    print(f"queued={item['instance_id']}", flush=True)
for evaluation in evaluate_pending(pending, args):
    evaluated[evaluation["instance_id"]] = evaluation
    save_jsonl(state_path, evaluated)
    print(json.dumps(evaluation, ensure_ascii=False), flush=True)
```

The main thread remains the only state writer.

- [ ] **Step 3: Run the focused tests and verify GREEN**

Run:

```bash
cd benchmark/swebench-lite-20260726
python3 -m unittest -v test_watch_evaluator.WatchEvaluatorTest.test_evaluates_pending_with_bounded_concurrency test_watch_evaluator.WatchEvaluatorTest.test_worker_exception_becomes_evaluator_error
```

Expected: both tests pass.

- [ ] **Step 4: Run the complete watcher test module**

Run:

```bash
cd benchmark/swebench-lite-20260726
python3 -m unittest -v test_watch_evaluator.py
```

Expected: all existing and new tests pass.

- [ ] **Step 5: Commit the implementation**

```bash
git add benchmark/swebench-lite-20260726/watch_evaluator.py benchmark/swebench-lite-20260726/test_watch_evaluator.py
git commit -m "benchmark: parallelize live SWE-bench evaluation"
```

### Task 3: Restart And Verify Five Workers

**Files:**
- Runtime state only: `/tmp/mycode-swe-full/evaluator-watch/evaluation-results.jsonl`

- [ ] **Step 1: Capture current progress and stop the evaluator session**

Capture the pane and state count, then send `C-c` only to `swe-evaluator`. Leave `swe-runner` running.

```bash
tmux -S /tmp/mycode-swe-tmux.sock capture-pane -p -J -t swe-evaluator:0.0 -S -30
tmux -S /tmp/mycode-swe-tmux.sock send-keys -t swe-evaluator:0.0 C-c
```

- [ ] **Step 2: Remove only interrupted evaluator containers**

List containers whose names start with `sweb.eval` and remove only containers belonging to the active `mycode-full-20260728` prefix after confirming their evaluator results are absent from the state file.

- [ ] **Step 3: Restart the evaluator with five workers**

```bash
tmux -S /tmp/mycode-swe-tmux.sock new-session -d -s swe-evaluator \
  -c /Users/fengrui03/Desktop/FFCode \
  "python3 -u benchmark/swebench-lite-20260726/watch_evaluator.py \
  --agent-state /tmp/mycode-swe-full/agent-results.jsonl \
  --root /tmp/mycode-swe-full/evaluator-watch \
  --harness /tmp/swebench-harness.ImQnZk \
  --harness-python /tmp/swebench-harness.ImQnZk/.venv/bin/python \
  --report-dir /Users/fengrui03/Desktop/FFCode/benchmark/swebench-lite-20260726/live-reports \
  --run-prefix mycode-full-20260728 --expected 300 --poll-seconds 5 \
  --timeout 1800 --max-attempts 3 --retry-delay 30 --workers 5"
```

- [ ] **Step 4: Verify runtime concurrency and persistence**

Confirm the evaluator tmux session remains alive, five distinct active containers are present, and evaluation state grows without duplicate IDs:

```bash
tmux -S /tmp/mycode-swe-tmux.sock capture-pane -p -J -t swe-evaluator:0.0 -S -30
docker ps --format '{{.Names}}' --filter name=sweb.eval
python3 -c 'import json; p="/tmp/mycode-swe-full/evaluator-watch/evaluation-results.jsonl"; rows=[json.loads(x) for x in open(p) if x.strip()]; print(len(rows), len({x["instance_id"] for x in rows}))'
```

Expected: five active evaluator cases while backlog exists, and both printed state counts are equal.
