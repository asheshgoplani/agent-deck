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


class TestWorkerPromptSkeletonFirstReading(unittest.TestCase):
    """The worker must narrow the repository before reading full bodies."""

    @classmethod
    def setUpClass(cls) -> None:
        """Load the shared prompt once for the skeleton-first assertions."""
        cls.text = WORKER_PROMPT.read_text(encoding="utf-8")

    def test_reading_stages_are_present_and_ordered(self) -> None:
        """Require the funnel's three stages in their execution order."""
        stages = [
            "Repository tree",
            "Declaration skeletons",
            "Narrowed full code",
        ]
        positions = [self.text.find(stage) for stage in stages]
        self.assertTrue(
            all(position >= 0 for position in positions),
            f"Worker contract must name every skeleton-first stage: {stages}",
        )
        self.assertEqual(
            positions,
            sorted(positions),
            "Worker contract must order tree, skeletons, then narrowed full code",
        )

    def test_full_file_reads_are_forbidden_before_narrowing(self) -> None:
        """Keep full bodies gated behind tree and skeleton inspection."""
        self.assertIn(
            "Do not read full source files before completing stages 1 and 2",
            self.text,
            "Without an explicit gate, workers can bypass skeleton-first reading",
        )

    def test_protocol_gives_grep_first_commands(self) -> None:
        """Bind each discovery command to its stage before full-code reads."""
        tree_start = self.text.index("Repository tree")
        skeleton_start = self.text.index("Declaration skeletons")
        full_code_start = self.text.index("Narrowed full code")

        tree_instructions = self.text[tree_start:skeleton_start]
        skeleton_instructions = self.text[skeleton_start:full_code_start]
        self.assertIn("rg --files --hidden -g '!.git'", tree_instructions)
        self.assertIn("rg -n", skeleton_instructions)


if __name__ == "__main__":
    unittest.main(verbosity=2)
