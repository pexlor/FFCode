import json
import unittest

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


if __name__ == "__main__":
    unittest.main()
