import argparse
import json
import tempfile
import unittest
from pathlib import Path

import run as benchmark_run


class ProtocolEventParserTest(unittest.TestCase):
    def test_model_text_containing_done_does_not_finish_turn(self):
        parser = benchmark_run.ProtocolEventParser()
        line = json.dumps({
            "version": 1,
            "type": "text_delta",
            "data": {"text": "summary of what was done: keep working"},
        }) + "\n"

        self.assertEqual(parser.feed(line), [])

    def test_recovers_turn_finished_event_split_across_chunks(self):
        parser = benchmark_run.ProtocolEventParser()
        line = json.dumps({
            "version": 1,
            "type": "turn_finished",
            "data": {"status": "completed", "stop_reason": "end_turn"},
        }) + "\n"
        split = len(line) // 2

        self.assertEqual(parser.feed(line[:split]), [])
        self.assertEqual(parser.feed(line[split:]), [
            {"status": "completed", "stop_reason": "end_turn"},
        ])

    def test_classifies_terminal_statuses_without_collapsing_them(self):
        parser = benchmark_run.ProtocolEventParser()
        events = []
        for status, reason in (
            ("completed", "end_turn"),
            ("incomplete", "max_tokens"),
            ("failed", "agent_error"),
            ("cancelled", "cancelled"),
        ):
            events.extend(parser.feed(json.dumps({
                "version": 1,
                "type": "turn_finished",
                "data": {"status": status, "stop_reason": reason},
            }) + "\n"))

        self.assertEqual(events, [
            {"status": "completed", "stop_reason": "end_turn"},
            {"status": "incomplete", "stop_reason": "max_tokens"},
            {"status": "failed", "stop_reason": "agent_error"},
            {"status": "cancelled", "stop_reason": "cancelled"},
        ])

    def test_ignores_diagnostics_and_unknown_protocol_versions(self):
        parser = benchmark_run.ProtocolEventParser()
        text = "configuration warning\n" + json.dumps({
            "version": 2,
            "type": "turn_finished",
            "data": {"status": "completed", "stop_reason": "end_turn"},
        }) + "\n"

        self.assertEqual(parser.feed(text), [])


class AgentBackendTest(unittest.TestCase):
    def test_builds_codex_exec_command_for_noninteractive_workspace(self):
        args = argparse.Namespace(
            agent="codex",
            binary=Path("/opt/codex"),
            model="gpt-5.6-sol",
        )

        command = benchmark_run.build_agent_command(args, Path("/tmp/case"))

        self.assertEqual(command, [
            "/opt/codex",
            "exec",
            "--json",
            "--ephemeral",
            "--sandbox",
            "workspace-write",
            "--model",
            "gpt-5.6-sol",
            "--cd",
            "/tmp/case",
            "-",
        ])

    def test_builds_existing_ffcode_command(self):
        args = argparse.Namespace(
            agent="ffcode",
            binary=Path("/opt/ffcode"),
            model=None,
        )

        command = benchmark_run.build_agent_command(args, Path("/tmp/case"))

        self.assertEqual(command, [
            "/opt/ffcode",
            "--cwd",
            "/tmp/case",
            "--output-format",
            "jsonl",
        ])

    def test_codex_success_is_determined_by_process_exit(self):
        self.assertEqual(
            benchmark_run.classify_agent_status(
                agent="codex",
                timed_out=False,
                returncode=0,
                turn_status="",
            ),
            "completed",
        )
        self.assertEqual(
            benchmark_run.classify_agent_status(
                agent="codex",
                timed_out=False,
                returncode=1,
                turn_status="",
            ),
            "agent_error",
        )

    def test_prediction_uses_configured_model_name(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            patch = root / "case.patch"
            patch.write_text("diff --git a/a b/a\n")
            output = root / "predictions.jsonl"
            tasks = [{"instance_id": "case-1"}]
            state = {"case-1": {"patch_path": str(patch)}}

            benchmark_run.write_predictions(
                output,
                tasks,
                state,
                model_name="Codex-gpt-5.6-sol",
            )

            prediction = json.loads(output.read_text())
            self.assertEqual(prediction["model_name_or_path"], "Codex-gpt-5.6-sol")
            self.assertEqual(prediction["model_patch"], patch.read_text())


if __name__ == "__main__":
    unittest.main()
