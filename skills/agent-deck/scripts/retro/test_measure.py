#!/usr/bin/env python3
"""Synthetic, local regression checks; never read live agent data."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import measure


class MeasureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="deck-retro-test-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)

    def write_records(self, name, records):
        path = self.root / name
        path.write_text("".join(json.dumps(row) + "\n" for row in records))
        return str(path)

    def run_measure(self, config, expected_exit=0):
        config_path = self.root / "config.json"
        config_path.write_text(json.dumps(config))
        output = self.root / "output"
        result = subprocess.run(
            [sys.executable, str(Path(measure.__file__)),
             "--config", str(config_path), "--start", "2026-01-01T00:00:00Z",
             "--end", "2026-01-02T00:00:00Z", "--out", str(output)],
            capture_output=True, text=True, timeout=20,
        )
        self.assertEqual(result.returncode, expected_exit, result.stderr)
        if expected_exit:
            return result
        return json.loads((output / "metrics.json").read_text())

    def test_five_digit_fraction_keeps_instant(self):
        parsed = measure.timestamp("2026-01-01T01:02:03.34515+02:00")
        self.assertEqual(parsed.microsecond, 345150)
        self.assertEqual(parsed.utcoffset().total_seconds(), 7200)

    def test_missing_and_malformed_sources_are_unknown(self):
        malformed = self.root / "bad.jsonl"
        malformed.write_text('{bad}\n{"unsupported":true}\n')
        for sources in ([str(self.root / "missing*.jsonl")], [str(malformed)]):
            with self.subTest(sources=sources):
                result = self.run_measure({"bus": sources, "journals": sources})
                self.assertIsNone(result["metrics"]["committed_records"])
                self.assertIsNone(result["metrics"]["journal_entries"])
                self.assertEqual(result["coverage"]["bus"]["status"], "unavailable")
        result = self.run_measure({"transcripts": {"worker": str(malformed)}})
        self.assertIsNone(result["parents"]["worker"]["wakes"])

    def test_journal_unknown_identity_stays_in_denominator(self):
        stamp = "2026-01-01T01:00:00Z"
        journal = self.write_records("journal.jsonl", [
            {"ts": stamp, "child": "fixture-child", "uuid": "fixture-turn"},
            {"ts": stamp, "child": "fixture-child", "uuid": "fixture-turn"},
            {"ts": stamp, "child": "fixture-child"},
            {"ts": stamp, "uuid": "orphan-turn"},
            {"ts": stamp},
        ])
        result = self.run_measure({"journals": [journal]})
        metrics = result["metrics"]
        self.assertEqual(metrics["journal_entries"], 5)
        self.assertEqual(metrics["duplicate_journal_entries"], 1)
        self.assertEqual(metrics["journal_unknown_keys"], 3)
        self.assertEqual(metrics["duplicate_journal_pct"], 20)

    def test_unsafe_label_is_rejected_before_read(self):
        result = self.run_measure({"transcripts": {"../escaped": "unused"}}, 2)
        self.assertIn("safe filenames", result.stderr)
        self.assertFalse((self.root / "escaped.json").exists())

    def test_latency_requires_remote_origin_and_nonnegative_clock(self):
        records = []
        for origin, seen in [
            ("local", "2026-01-01T01:00:01Z"),
            ("remote-fixture", "2026-01-01T00:59:59Z"),
            ("remote-fixture", "2026-01-01T01:00:02Z"),
        ]:
            records.append({"ts": "2026-01-01T01:00:00Z", "kind": "turn", "data": {
                "kind": "turn", "origin": origin, "t_seen": seen,
                "t_signal": "2026-01-01T01:00:00Z",
            }})
        ledger = self.write_records("ledger.jsonl", records)
        result = self.run_measure({"ledger": [ledger], "remote_origins": ["remote-fixture"]})
        self.assertEqual(result["metrics"]["cross_host_latency_samples"], 1)
        self.assertEqual(result["metrics"]["cross_host_latency_ms"], [2000])
        self.assertEqual(result["metrics"]["cross_host_clock_errors"], 1)

    def test_phone_report_includes_parent_cost_and_rate(self):
        transcript = self.write_records("transcript.jsonl", [
            {"type": "user", "timestamp": "2026-01-01T00:00:00Z",
             "message": {"content": "Synthetic request"}},
            {"type": "assistant", "timestamp": "2026-01-01T00:00:01Z",
             "message": {"id": "message-fixture", "model": "fixture",
                         "usage": {"input_tokens": 19, "output_tokens": 4},
                         "content": [{"type": "text", "text": "Synthetic response"}]}},
        ])
        result = self.run_measure({"transcripts": {"worker-fixture": transcript}})
        self.assertEqual(result["parents"]["worker-fixture"]["tokens"], 23)
        report = (self.root / "output" / "report.html").read_text()
        self.assertIn("worker-fixture", report)
        self.assertIn("tokens: 23", report)
        self.assertIn("wakes per hour:", report)


if __name__ == "__main__":
    unittest.main()
