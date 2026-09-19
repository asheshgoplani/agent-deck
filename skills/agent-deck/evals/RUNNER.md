# Running these evals

Written per Anthropic's skill-creator method (`~/.claude/plugins/marketplaces/anthropic-agent-skills/skills/skill-creator/SKILL.md`).

## Trigger evals (`trigger_evals` in evals.json)

The skill-creator's own optimizer (`scripts/run_loop.py`) shells out to `claude -p`, which is banned for this task (see PROMPT.md SAFETY/DELIVERABLE). Two offline alternatives, in order of preference:

1. **`claude plugin eval`** (if this checkout is loaded as a plugin): point it at this skill and `evals/evals.json`'s `trigger_evals` block; it does not require `claude -p`.
2. **Manual inline judgment** (what this refresh actually did): for each `should_trigger`/`should_not_trigger` query, compare it against the skill's frontmatter `description` and judge whether a reasonable triggering policy would consult this skill. This is weaker evidence than a real subagent run (no held-out test, no repeated sampling) but catches an obviously miscalibrated description without spawning processes.

## Task evals (`evals` in evals.json)

Each has a `prompt` and a checkable `expected_output`. To score them for real: spawn a with-skill subagent per eval (Task/Agent tool, not `claude -p`) pointed at this skill directory, save its transcript, and grade the transcript against `expected_output` — same procedure as skill-creator's "Running and evaluating test cases" section, minus the `claude -p`-based description optimizer. This refresh authored the prompts and assertions and reasoned through expected behavior against the verified CLI truth table, but did not spawn the full with-skill/baseline subagent matrix (out of scope for a docs-and-evals-authoring pass at this budget) — see RESULTS.md for exactly what ran.

## Agent-friendliness evals (`agent_friendliness_evals` in evals.json, agent-deck skill only)

Added per maintainer request. These check structured-output discipline (reads `confirmation`, not text), non-TTY safety (`--verify` needs `--yes` off a TTY), and the performance-budget claims in `agent-deck health`. Score the same way as task evals: run the prompt with the skill loaded, check the transcript against `expected_output`.
