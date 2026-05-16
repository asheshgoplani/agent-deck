"""Regression tests for issue #971.

Heartbeat NEED: lines repeated unchanged for 12-21 hours when the user did
not respond. There was no de-duplication, no escalation tactic, and no
auto-retire. This module pins the fix: after a configurable threshold
(default 3) of consecutive identical NEED: lines, the bridge must either
escalate via a distinct "STILL BLOCKED" tactic (one-shot) or drop the line
from heartbeat alerts on subsequent cycles.
"""

from __future__ import annotations

import sys
from pathlib import Path

import pytest

sys.path.insert(0, str(Path(__file__).parent.parent))

from bridge import filter_need_lines  # noqa: E402  pylint: disable=wrong-import-position


def _resp(*need_lines: str) -> str:
    """Build a conductor reply that contains the given NEED: lines."""
    body = ["[STATUS] Auto-responded to 0 sessions. 1 needs your attention."]
    body.extend(need_lines)
    return "\n".join(body)


class TestHeartbeatRetiresIdenticalNeedRegressionFor971:
    """Pin the fix for issue #971: identical NEED lines must retire."""

    def test_cycle_one_passes_need_through(self):
        result = filter_need_lines(
            _resp("NEED: api-fix - decide test environment"),
            prev_counts={},
        )
        assert result["alerts"] == ["NEED: api-fix - decide test environment"]
        assert result["retired"] == []
        assert result["counts"] == {"NEED: api-fix - decide test environment": 1}

    def test_cycle_two_still_passes_need_through(self):
        result = filter_need_lines(
            _resp("NEED: api-fix - decide test environment"),
            prev_counts={"NEED: api-fix - decide test environment": 1},
        )
        assert result["alerts"] == ["NEED: api-fix - decide test environment"]
        assert result["retired"] == []
        assert result["counts"] == {"NEED: api-fix - decide test environment": 2}

    def test_cycle_three_retires_with_escalation_marker(self):
        """3rd consecutive identical NEED: must escalate distinctly, NOT repeat verbatim."""
        result = filter_need_lines(
            _resp("NEED: api-fix - decide test environment"),
            prev_counts={"NEED: api-fix - decide test environment": 2},
        )
        # The plain NEED line must NOT be forwarded as-is on the 3rd cycle.
        assert "NEED: api-fix - decide test environment" not in result["alerts"]
        # An escalation/retire notice must be emitted instead.
        assert len(result["retired"]) == 1
        retire_line = result["retired"][0]
        assert "STILL BLOCKED" in retire_line or "RETIRED" in retire_line
        assert "api-fix" in retire_line  # carries the original NEED context
        assert result["counts"]["NEED: api-fix - decide test environment"] == 3

    def test_cycle_four_drops_stale_need_entirely(self):
        """After retire, subsequent identical NEED lines are silently dropped."""
        result = filter_need_lines(
            _resp("NEED: api-fix - decide test environment"),
            prev_counts={"NEED: api-fix - decide test environment": 3},
        )
        assert result["alerts"] == []
        assert result["retired"] == []  # already retired previously, no re-escalation
        assert result["counts"]["NEED: api-fix - decide test environment"] == 4

    def test_new_need_line_after_repeats_still_passes_through(self):
        """A fresh, different NEED line is not blocked by a stale repeating one."""
        result = filter_need_lines(
            _resp(
                "NEED: api-fix - decide test environment",
                "NEED: web-build - npm error on Node 22",
            ),
            prev_counts={"NEED: api-fix - decide test environment": 5},
        )
        assert "NEED: web-build - npm error on Node 22" in result["alerts"]
        # Stale one is dropped, only the fresh one is forwarded:
        assert "NEED: api-fix - decide test environment" not in result["alerts"]
        assert result["counts"]["NEED: web-build - npm error on Node 22"] == 1

    def test_resolved_need_drops_from_state(self):
        """When the conductor stops emitting a NEED line, its count is cleared."""
        result = filter_need_lines(
            _resp("NEED: web-build - npm error on Node 22"),
            prev_counts={"NEED: api-fix - decide test environment": 2},
        )
        assert "NEED: api-fix - decide test environment" not in result["counts"]
        assert result["counts"]["NEED: web-build - npm error on Node 22"] == 1

    def test_response_without_any_need_lines_yields_empty(self):
        result = filter_need_lines(
            "[STATUS] All clear, nothing waiting.",
            prev_counts={"NEED: api-fix - decide test environment": 2},
        )
        assert result["alerts"] == []
        assert result["retired"] == []
        assert result["counts"] == {}

    def test_threshold_is_configurable(self):
        """Caller can lower threshold (e.g. for impatient policies)."""
        result = filter_need_lines(
            _resp("NEED: foo"),
            prev_counts={"NEED: foo": 1},
            threshold=2,
        )
        assert result["alerts"] == []
        assert len(result["retired"]) == 1
        assert "foo" in result["retired"][0]

    def test_multiple_lines_independent_state(self):
        """Each NEED line has its own retire counter."""
        result = filter_need_lines(
            _resp("NEED: A", "NEED: B"),
            prev_counts={"NEED: A": 2, "NEED: B": 0},
        )
        # A hits threshold this cycle -> retired
        assert "NEED: A" not in result["alerts"]
        assert any("A" in r for r in result["retired"])
        # B is on its first cycle -> normal alert
        assert "NEED: B" in result["alerts"]


if __name__ == "__main__":
    sys.exit(pytest.main([__file__, "-v"]))
