# Running these evals

Written per Anthropic's skill-creator method. One skill, one file: `evals.json` holds the evals for every area of the agent-deck skill, tagged with `area` (core, fleet, session-share, recall, retro, repro; plus `agent_friendliness_evals`). Fleet, session-share, recall, retro and repro were separate skills until 2026-10-20; their evals moved here with them.

Never use `claude -p` (or the skill-creator's `run_loop.py`, which shells out to it). `cmd/agent-deck/skill_layout_docs_test.go` keeps the file well formed: unique ids, prompt and expected_output on each eval, at least two evals per area.

## Task evals (`evals`, `agent_friendliness_evals`)

Score them as a with/without pair, fresh agents per run (Workflow or Agent sub-agents):

1. **With skill:** the agent reads `SKILL.md` first, then the references it points to for the task.
2. **Without skill:** the agent may not read `SKILL.md`, `skills/`, plugin caches, repo docs or source; only `agent-deck <cmd> --help`.
3. Both are answer-only: they write the commands they would run, never run state-changing commands.
4. A separate grader sees both answers blinded (labels swapped between batches) and grades each against `expected_output`: pass only when every key element is there and nothing wrong is recommended.

An area whose with-skill pass rate does not beat its without-skill rate gets fixed or dropped. Record each run's numbers and date with the change that ran them.

## Trigger evals (`trigger_evals`)

A judge agent sees only the frontmatter `description` and decides for each query whether to load the skill; compare with `should_trigger`. This is weaker than sampling a real triggering policy, but catches a description that misses an area. `claude plugin eval` is the stronger option when this checkout is loaded as a plugin.
