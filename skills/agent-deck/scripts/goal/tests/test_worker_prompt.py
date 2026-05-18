#!/usr/bin/env python3
"""Smoke test for the goal worker prompt template.

Confirms the trust-but-verify pattern baked in after the 2026-05-18 incident
(PR #885 over-claim + ux-rethink false-positive + goal-framework metronome
wakes) is still present in the contract prompt. The keywords are the contract
itself — losing them silently would re-introduce the metronome failure mode.

Run from anywhere:
    python3 -m unittest discover -s tests -v
"""
from __future__ import annotations

import unittest
from pathlib import Path


WORKER_PROMPT = (
    Path(__file__).resolve().parent.parent / "prompts" / "worker.md"
)


class TestWorkerPromptTrustButVerify(unittest.TestCase):
    """The worker contract MUST carry the priority-0 trust-but-verify rule.

    These assertions are intentionally string-level: the prompt is consumed
    verbatim by a fresh Claude session every wake, so the literal wording
    *is* the contract. A refactor that loses the keywords loses the rule.
    """

    @classmethod
    def setUpClass(cls) -> None:
        cls.text = WORKER_PROMPT.read_text(encoding="utf-8")

    def test_prompt_file_exists(self) -> None:
        self.assertTrue(
            WORKER_PROMPT.exists(),
            f"worker prompt template missing at {WORKER_PROMPT}",
        )

    def test_priority_zero_keyword_present(self) -> None:
        self.assertIn(
            "PRIORITY 0",
            self.text,
            "Worker contract must include 'PRIORITY 0' header — this is the "
            "trust-but-verify rule baked in after 2026-05-18. Without it, "
            "wakes regress to metronome status-only heartbeats.",
        )

    def test_trust_but_verify_keyword_present(self) -> None:
        # Case-insensitive — the section can be titled either way, but the
        # phrase has to appear so future maintainers can grep for it.
        self.assertIn(
            "trust-but-verify",
            self.text.lower(),
            "Worker contract must reference 'trust-but-verify' so the rule "
            "is greppable and the link to the SKILL.md section is intact.",
        )

    def test_priority_zero_appears_before_step_one(self) -> None:
        idx_p0 = self.text.find("PRIORITY 0")
        idx_step1 = self.text.find("### 1. Recall context")
        self.assertGreater(idx_p0, 0, "PRIORITY 0 section not found")
        self.assertGreater(idx_step1, 0, "Step 1 section not found")
        self.assertLess(
            idx_p0,
            idx_step1,
            "PRIORITY 0 must come BEFORE step 1 — otherwise the worker takes "
            "a new bounded step before verifying last cycle's claim, "
            "defeating the entire pattern.",
        )

    def test_ground_truth_verifier_examples_present(self) -> None:
        # At least one concrete primary-source command must appear so the
        # worker has a template to imitate, not just abstract guidance.
        candidates = ["gh pr view", "gh release view", "gh api", "gh issue view"]
        hits = [c for c in candidates if c in self.text]
        self.assertTrue(
            hits,
            "Worker contract must include at least one concrete `gh` "
            f"primary-source command from {candidates}. Found none — "
            "abstract guidance without examples regresses to vibes.",
        )


if __name__ == "__main__":
    unittest.main(verbosity=2)
