#!/usr/bin/env python3
"""Regression tests for issue #2946.

The launchd plist and systemd unit send the bridge's stdout and stderr to
<conductor>/bridge.log. The bridge also added its own FileHandler for that
same file, so under a daemon every log record was written to bridge.log
twice. The bridge now skips its FileHandler when stdout already is
bridge.log, and still writes the file when run by hand from a terminal.

Each test loads a fresh copy of the canonical bridge (logging is set up at
import time) with AGENT_DECK_CONDUCTOR_DIR pointing at a temp dir and
sys.stdout replaced, emits one record, and counts it in bridge.log.
Plain unittest, so it runs under pytest (like the other conductor tests)
and with `python3 conductor/tests/test_issue2946_bridge_log_once.py`.
"""

from __future__ import annotations

import importlib.util
import io
import logging
import os
import sys
import tempfile
import types
import unittest
from pathlib import Path
from unittest import mock

BRIDGE_PATH = (
    Path(__file__).resolve().parents[2] / "internal" / "session" / "conductor_bridge.py"
)
MARKER = "issue2946-marker-record"


def _emit_with_fresh_bridge(conductor_dir: Path, stdout) -> None:
    """Import a fresh bridge with the given stdout, log MARKER once, then
    close the handlers it installed and restore the root logger."""
    root = logging.getLogger()
    saved_handlers, saved_level = root.handlers[:], root.level
    root.handlers = []
    extra_modules = {}
    if importlib.util.find_spec("toml") is None:
        # toml is only used when loading config, not at import time.
        extra_modules["toml"] = types.ModuleType("toml")
    try:
        with mock.patch.dict(os.environ, {"AGENT_DECK_CONDUCTOR_DIR": str(conductor_dir)}), \
                mock.patch.dict(sys.modules, extra_modules), \
                mock.patch.object(sys, "stdout", stdout):
            spec = importlib.util.spec_from_file_location("bridge_issue2946", BRIDGE_PATH)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            module.log.info(MARKER)
            for handler in root.handlers:
                handler.flush()
                if isinstance(handler, logging.FileHandler):
                    handler.close()
    finally:
        root.handlers = saved_handlers
        root.setLevel(saved_level)


class BridgeLogOnceTest(unittest.TestCase):
    def setUp(self):
        self._tmp = tempfile.TemporaryDirectory()
        self.conductor_dir = Path(self._tmp.name)
        self.log_path = self.conductor_dir / "bridge.log"

    def tearDown(self):
        self._tmp.cleanup()

    def _marker_count(self) -> int:
        return self.log_path.read_text(encoding="utf-8").count(MARKER)

    def test_daemon_stdout_is_bridge_log_writes_each_record_once(self):
        # launchd StandardOutPath / systemd StandardOutput=append: bridge.log
        with open(self.log_path, "a", encoding="utf-8") as stdout:
            _emit_with_fresh_bridge(self.conductor_dir, stdout)
        self.assertEqual(self._marker_count(), 1, self.log_path.read_text(encoding="utf-8"))

    def test_manual_run_to_terminal_still_writes_bridge_log(self):
        stdout = io.StringIO()  # no fileno(), like a captured or odd stream
        _emit_with_fresh_bridge(self.conductor_dir, stdout)
        self.assertEqual(self._marker_count(), 1)
        self.assertEqual(stdout.getvalue().count(MARKER), 1)

    def test_manual_run_with_other_stdout_file_still_writes_bridge_log(self):
        other = self.conductor_dir / "other.out"
        with open(other, "w", encoding="utf-8") as stdout:
            _emit_with_fresh_bridge(self.conductor_dir, stdout)
        self.assertEqual(self._marker_count(), 1)
        self.assertEqual(other.read_text(encoding="utf-8").count(MARKER), 1)


if __name__ == "__main__":
    unittest.main()
