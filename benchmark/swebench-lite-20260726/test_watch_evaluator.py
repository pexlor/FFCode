import argparse
import json
import tempfile
import threading
import unittest
from pathlib import Path

import watch_evaluator


class WatchEvaluatorTest(unittest.TestCase):
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

    def test_classifies_new_results_without_repeating_completed_evaluations(self):
        with tempfile.TemporaryDirectory() as directory:
            patch = Path(directory) / "case.patch"
            patch.write_text("nonempty")
            results = {
                "with-patch": {"instance_id": "with-patch", "patch_bytes": 8, "patch_path": str(patch)},
                "empty": {"instance_id": "empty", "patch_bytes": 0},
                "done": {"instance_id": "done", "patch_bytes": 8, "patch_path": str(patch)},
            }
            evaluated = {"done": {"instance_id": "done", "status": "resolved"}}

            pending, skipped = watch_evaluator.classify_results(results, evaluated)

            self.assertEqual([item["instance_id"] for item in pending], ["with-patch"])
            self.assertEqual([item["instance_id"] for item in skipped], ["empty"])

    def test_writes_one_prediction_with_patch_contents(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            patch = root / "case.patch"
            patch.write_text("diff --git a/a b/a\n")
            output = root / "prediction.jsonl"

            watch_evaluator.write_prediction(
                output,
                {"instance_id": "case-1", "patch_path": str(patch)},
                "FFCode-MiniMax-M3",
            )

            rows = [json.loads(line) for line in output.read_text().splitlines()]
            self.assertEqual(len(rows), 1)
            self.assertEqual(rows[0]["instance_id"], "case-1")
            self.assertEqual(rows[0]["model_patch"], patch.read_text())

    def test_missing_patch_is_classified_as_skipped(self):
        result = {"instance_id": "missing", "patch_bytes": 100, "patch_path": "/missing.patch"}

        pending, skipped = watch_evaluator.classify_results({"missing": result}, {})

        self.assertEqual(pending, [])
        self.assertEqual([item["instance_id"] for item in skipped], ["missing"])

    def test_retryable_runner_error_is_not_consumed_as_empty_patch(self):
        result = {"instance_id": "retryable", "status": "runner_error", "patch_bytes": 0}

        pending, skipped = watch_evaluator.classify_results({"retryable": result}, {})

        self.assertEqual(pending, [])
        self.assertEqual(skipped, [])


if __name__ == "__main__":
    unittest.main()
