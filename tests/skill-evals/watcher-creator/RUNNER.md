# Running the watcher-creator evals

The skill lives in `cmd/agent-deck/assets/skills/watcher-creator/` (embedded and installed by
`agent-deck watcher install-skill watcher-creator`). Its evals live here, outside the skill
directory, because `go:embed assets/skills/watcher-creator/*` would ship them into the user's
skill pool. They use the agent-deck skill's checker, `skills/agent-deck/evals/check.py`, in the
same design: see [its RUNNER.md](../../../skills/agent-deck/evals/RUNNER.md). Never use `claude -p`.

| File | What it is |
|---|---|
| `promises.json` | What the skill teaches: one entry per teaching unit (81), each with its heading and a verbatim `anchor` |
| `evals.json` | 37 task evals, 5 agent-friendliness evals, 18 trigger evals (10 should, 8 should-not, 5 of them near misses); areas `core`, `adapters`, `validation` |

## Commands

From the repo root (`--binary` is a build of this checkout, e.g. `go build -o /tmp/agent-deck ./cmd/agent-deck`):

```bash
C=skills/agent-deck/evals/check.py
D="--skill-dir cmd/agent-deck/assets/skills/watcher-creator --evals-dir tests/skill-evals/watcher-creator"
python3 -I $C static   $D --binary /tmp/agent-deck   # schema, anchors, grounding, triggers, coverage, CLI drift
python3 -I $C mutate   $D --binary /tmp/agent-deck   # delete cited sections / garble trigger terms: all must die
python3 -I $C facts    $D                            # change each pinned fact in place (report only)
python3 -I $C coverage $D                            # promise coverage table (--md for markdown)
python3 -I $C prompts  $D                            # prompts only, for a model run
python3 -I $C grade answers.json $D                  # grade model answers
```

`go test ./cmd/agent-deck/` runs `static` and `mutate` for this skill too (`skill_layout_docs_test.go`),
without the CLI drift check, and `TestWatcherCreatorSkill_Parseable` checks the frontmatter and that the
body names all five adapter types and `agent-deck watcher create`.

## Task evals with a model

Same protocol as the agent-deck skill: answer-only sub-agents (Workflow or Agent), never headless
`claude -p`, never a live `agent-deck watcher create`.

1. **With skill:** the agent reads only a copy of `SKILL.md`, never this directory, and writes the commands and facts it would give the user.
2. **Without skill:** the agent may run only `agent-deck watcher <cmd> --help` (in a throwaway HOME).
3. Save `{"answers": [{"id": 1, "answer": "..."}]}` and run `check.py grade`. An answer passes when every `must` matches and no `must_not` matches.

Three with-skill runs: an eval passing 1 or 2 of 3 is **flaky**. An eval passing without the skill is **weak**:
tighten it, or mark it `help_answerable` (the `watcher --help` page already shows the webhook, github,
ntfy `--random-topic` and `NTFY_TOKEN` examples, so evals 1, 11, 12 and 15 are the likely candidates).

## Trigger evals

A judge agent sees only the frontmatter `description` and decides per query whether to load the skill.
Near misses (writing a webhook server, GitHub Actions, Slack bots, Gmail filters, conductor setup) are
checked against its `Not for ...` clause.

## What the deterministic checks prove

- Every promise's anchor is still under its heading, every promise is covered by a task eval, and
  every `must` matches its cited section (`SKILL.md#<heading>`).
- Each eval has a `must` that matches at most 12 times in the skill and no `must` the prompt satisfies.
- `mutate`: deleting an eval's cited sections makes it fail; garbling a trigger term out of the
  description makes that trigger eval fail; an invented command, subcommand and flag are caught.
- With `--binary`, every `agent-deck ...` command and flag in `SKILL.md` exists in that binary's `--help`.
- `facts` (last run: 42/42 evals pin a fact; 135 in-place mutants, 96 killed, 71%).

### What they do not prove

- That the facts are true. The checks compare the evals with the skill text, not with the Go source:
  a wrong default (say `18461` written as the webhook port) passes as long as the eval says the same.
  The facts were checked against `internal/watcher` and `cmd/agent-deck/watcher_cmd.go` by hand on
  2026-11-02; nothing re-checks them.
- A fact changed in place survives where a `must` matches more than once in its sources (the 39
  `facts` survivors: `X-Webhook-Sender`, `--secret-file`, `NTFY_TOKEN`, `[source]` and the like,
  each written both in prose and in a command).
- The CLI drift check sees only commands inside code spans and blocks, so a command named in plain
  prose is not checked. It checks names, not behaviour: that `watcher create webhook` really requires
  `--port`, or refuses an existing `watcher.toml`, is not run.
- Regexes grade answers by wording: an answer can name the right fact in words no `must` allows
  (fail), or a `must_not` negation guard can miss a wrong command written next to "not" (pass).
- The `watcher.toml` example and the Python signing snippet are never executed; nor is the gmail
  section's claim that the engine runs no gmail adapter re-checked when gmail support lands.
- Trigger evals with a judge that sees only the description are weaker than a real triggering policy.
