# Running these evals

Written per Anthropic's skill-creator method. See [../../agent-deck/evals/RUNNER.md](../../agent-deck/evals/RUNNER.md) for the full rationale (same runner approach applies here): `claude -p` is banned for this task, so the skill-creator's automated `run_loop.py` description-optimizer was not run; trigger evals were reasoned through manually against the frontmatter `description`, and task evals were authored with checkable `expected_output` but not scored via a full with-skill/baseline subagent matrix in this pass.
