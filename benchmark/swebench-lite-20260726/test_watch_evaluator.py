import json
import tempfile
import unittest
from pathlib import Path

import watch_evaluator


class WatchEvaluatorTest(unittest.TestCase):
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
                "MyCode-MiniMax-M3",
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


if __name__ == "__main__":
    unittest.main()
