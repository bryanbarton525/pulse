#!/usr/bin/env python3
"""Fixture regressions for the demo's evidence renderers."""

import importlib.util
import json
import subprocess
import sys
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path


HERE = Path(__file__).parent


def load(name, filename):
    spec = importlib.util.spec_from_file_location(name, HERE / filename)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


inspect = load("demo_inspect", "demo_inspect.py")


def record(path, body, **extra):
    return json.dumps({"path": path, "body": json.dumps(body), **extra})


class ShowActionsTests(unittest.TestCase):
    def run_viewer(self, lines, *args):
        return subprocess.run(
            [sys.executable, str(HERE / "show-actions.py"), *args],
            input="\n".join(lines) + "\n",
            text=True,
            capture_output=True,
            check=True,
        ).stdout

    def test_incident_filter_is_exact_not_prefix_matching(self):
        lines = [
            record("/v1/chat/completions", {"messages": [{"content": "# Incident inc-1"}]}),
            record("/v1/chat/completions", {"messages": [{"content": "# Incident inc-10"}]}),
        ]
        counts = json.loads(self.run_viewer(lines, "--incident", "inc-1", "--counts-json"))
        self.assertEqual(counts, {"llm": 1, "slack": 0, "slackWithInvestigation": 0, "observability": 0})

    def test_malformed_shapes_do_not_crash(self):
        lines = ["not json", record("/v1/chat/completions", None), record("/logs", [None, "noise"])]
        counts = json.loads(self.run_viewer(lines, "--counts-json"))
        self.assertEqual(counts, {"llm": 1, "slack": 0, "slackWithInvestigation": 0, "observability": 1})

    def test_credentials_are_never_rendered(self):
        secret = "do-not-print-this-token"
        lines = [record(
            "/slack/webhook",
            {"text": "Incident `inc-1`"},
            auth=f"Bearer {secret}",
        )]
        output = self.run_viewer(lines)
        self.assertNotIn(secret, output)
        self.assertIn("present (Bearer)", output)


class InspectTests(unittest.TestCase):
    def test_live_state_distinguishes_missing_fresh_and_stale(self):
        now = datetime.now(timezone.utc)
        self.assertEqual(inspect.live_state({}), "MISSING")
        self.assertTrue(inspect.live_state({"lastCheckTime": now.isoformat()}).startswith("fresh"))
        old = now - timedelta(seconds=inspect.STALE_SECONDS + 5)
        self.assertTrue(inspect.live_state({"lastCheckTime": old.isoformat()}).startswith("STALE"))

    def test_detector_distinguishes_warmup_from_real_zero(self):
        self.assertEqual(inspect.detector({"driftState": "warming", "driftSamples": 2}, "drift"), "warming (2)")
        self.assertEqual(inspect.detector({"driftState": "ready", "driftSamples": 8}, "drift"), "0.000 (8)")


if __name__ == "__main__":
    unittest.main()
