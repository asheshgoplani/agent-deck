# Running these evals

One skill, one eval set. Never use `claude -p` (or the skill-creator's `run_loop.py`, which shells out to it).

| File | What it is |
|---|---|
| `promises.json` | What the skill teaches: one entry per teaching unit, with the file, heading and a verbatim `anchor` that must stay in the text, plus `checked_by` for promises a deterministic checker covers |
| `evals.json` | Task evals (`evals`, `agent_friendliness_evals`) and trigger evals (`trigger_evals`) |
| `check.py` | Deterministic checks and grading. No model calls |
| `run.sh` | Runs every deterministic eval: `check.py static` and `mutate`, plus the script tests |
| `drift-allow.json` | Invocations the CLI drift check skips on purpose, each with a reason |

## One command

```bash
skills/agent-deck/evals/run.sh                 # builds this checkout for the CLI check; ~30 s
skills/agent-deck/evals/run.sh --answers a.json # also grade model answers (below)
```

`go test ./cmd/agent-deck/` runs `check.py static` and `mutate` too (`TestAgentDeckSkillEvalsDeterministic`), without the CLI check, which needs a built binary.

## What the deterministic checks prove

- `static`:
  - every promise's anchor is still under its heading;
  - every task eval lists the promises it checks, cites its sources (`file#Exact heading`) and has `checks.must` regexes, and every `must` regex matches the text of its sources;
  - trigger terms match the description (should trigger) or its "Not for" clause (near misses), and unrelated topics stay out of it;
  - every promise is covered by a task eval or a script/Go test, except reference tables (`kind: reference`), which may be covered by the drift checks alone; `static` and `coverage` report those as drift-only;
  - checks pin facts: no `must` is satisfied by the eval's own prompt, each eval has a `must` that matches at most 12 times across the skill, and cited headings are unique and narrower than a whole file;
  - every key in config-reference.md's toml blocks is a toml tag in the Go source;
  - with `--binary`, every `agent-deck ...` command and flag in the skill exists in that binary's `--help` (or, for a subcommand whose help is its parent's, in the CLI's flag definitions).
- `mutate`: in a temp copy, deletes each eval's cited sections and garbles each trigger eval's terms; every eval must then fail. It also injects an invented flag, command and config key, which the drift checks must catch.

### What they do not prove

- `mutate` deletes whole sections. A fact changed in place survives `static` when another cited source, or a sibling clause, still satisfies the `must`. Examples: a default flipped from 2s to 30s, or `confirmation:"unknown"` rewritten while cli-reference still says it. The model runs catch some of these; the drift checks catch none.
- Config defaults and values are not compared with the Go source: only the key names are checked.
- A flag listed in the root help is accepted on every subcommand.

## Task evals with a model

Answer-only runs by fresh sub-agents (Workflow or Agent), never headless `claude -p`:

1. **With skill:** the agent reads only a copy of the skill (SKILL.md first, then the references it needs), never `evals/`, and writes the commands and facts it would give the user.
2. **Without skill:** the agent may run only `agent-deck <cmd> --help`.
3. Save the answers as `{"answers": [{"id": 1, "answer": "..."}]}` and grade them with `check.py grade` (or `run.sh --answers`). An answer passes when every `must` regex matches and no `must_not` matches.

Repeat the with-skill run 3 times. An eval is **flaky** when it passes 1 or 2 times out of 3. It is **weak** when it passes without the skill: tighten its checks to a fact only the skill gives, or mark it `help_answerable` when the skill adds nothing to `--help`. Record each run's numbers, with the date and the change that ran them.

## Trigger evals

A judge agent sees only the frontmatter `description` and decides for each query whether to load the skill. Compare its decisions with `should_trigger`. This is weaker than sampling a real triggering policy, but it catches a description that misses an area. `claude plugin eval` is the stronger option when this checkout is loaded as a plugin.

## Adding or changing something

- **New section in a reference:** add a promise for it, with an anchor copied verbatim from the text. Then add an eval that lists the promise, cites the section, and uses 2-4 `must` regexes that each name one fact from that section.
- **Changed text under an eval:** run `run.sh`. A `must pattern not in its sources` failure means the fact moved or changed. Update the eval or the text, never just the regex.
