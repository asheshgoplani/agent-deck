# Running these evals

One skill, one eval set. Never use `claude -p` (or the skill-creator's `run_loop.py`, which shells out to it).

| File | What it is |
|---|---|
| `promises.json` | What the skill teaches: one entry per teaching unit, with the file, heading and a verbatim `anchor` that must stay in the text, plus `checked_by` for promises a deterministic checker covers |
| `evals.json` | Task evals (`evals`, `agent_friendliness_evals`) and trigger evals (`trigger_evals`) |
| `check.py` | Deterministic checks and grading. No model calls |
| `run.sh` | Runs every deterministic eval: `check.py static` and `mutate`, plus the script tests (`--flows` adds `flows.sh`) |
| `flows.sh` | Agent-flow evals: send, child completion and inbox against a real binary, real tmux and a fake Claude (below) |
| `flows/fake-claude` | The fake Claude those evals launch: writes a transcript and fires hooks, never calls a model |
| `flows/tmux-guard` | The `tmux` on their PATH: forwards only calls that name the private socket, refuses the rest |
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

## Agent-flow evals

```bash
skills/agent-deck/evals/flows.sh                      # builds this checkout; ~1 min
skills/agent-deck/evals/flows.sh --binary ./agent-deck --socket my-evals --only child-done-ok
skills/agent-deck/evals/run.sh --flows                # the skill evals plus these
```

They drive the flows a parent agent uses with its children, the way the skill teaches them, against a real `agent-deck` binary and a real tmux server. The agent is `flows/fake-claude`, a shell script that answers `EVAL_*` tokens: it writes the Claude transcript, runs `agent-deck hook-handler` for SessionStart, UserPromptSubmit and Stop, and prints the completion sentinel when asked. Never a model, never `claude -p`.

Isolation: a throwaway HOME and XDG dirs under a short `<work dir>/adflow.*` path (`--work-dir`, default `$TMPDIR` or `/tmp`; flows.sh refuses a work dir whose tmux socket path would pass 100 bytes), an allowlisted environment (`env -i`), `TMUX_TMPDIR` inside it, and `[tmux] socket_name` set to a private name (`--socket`, default `agent-deck-evals-<pid>`). The `tmux` on PATH is `flows/tmux-guard`: it forwards a call only when it names `-L <that socket>` (or is `tmux -V`), and refuses and logs anything else. The sandbox and the private server are removed on exit; the per-eval logs and `tmux-calls.log` stay in `--log-dir`.

| Eval | What it does | Passes when |
|---|---|---|
| `send-reply` | `add` + `session start` a parent, `session send` (default: waits for the composer) a token | `session output` shows the agent's reply to it |
| `child-done-ok` | `launch --parent` a child whose turn ends in `status=ok` | `session children --json` shows `done_status` ok and the summary; `inbox drain --json` returns that completion exactly once; after another `notify-daemon --once`, a second drain does not return it |
| `child-done-fail` | the same with `status=fail` | the same, with fail |
| `child-last-wins` | the child prints a fail sentinel, then an ok one, in one reply | ok with the second summary |
| `child-bad-sentinel` | the child prints the marker with `status=maybe` | no `done_status`, no completion in the inbox |
| `child-no-sentinel` | the child replies without the marker | no `done_status`, no completion in the inbox |
| `inbox-routing` | a second parent with its own child | the completion is listed and drained under its own parent only |
| `isolation` (always last) | reads `tmux-calls.log` | every agent-deck tmux call named the private socket (the rest are `-V`), none refused, the server socket is inside the sandbox, the eval sessions are on it, and the guard refuses a default-socket call |

### What they prove

- `session send` gets a message into a Claude-style pane and `session output` reads the answer back from the transcript.
- A child launched with `--parent` is linked to it; its sentinel, read from the transcript after its Stop hook, reaches `session children` and the parent's durable inbox through `notify-daemon --once`; ok and fail, the summary, and last-sentinel-wins all survive the whole path; a drained completion is not delivered again; a malformed or missing sentinel is never reported as a completion; one parent never receives another parent's child.
- With `[tmux] socket_name` set, every tmux call from the agent-deck processes in the sandbox (CLI, notifier, the hook handler run from the pane) names that `-L` socket (checked on every run from the guard log, not assumed).

### What they do not prove

- Real Claude Code. The fake draws a Claude-like composer and writes transcript lines in the shape agent-deck reads; a change in Claude's real screen, transcript or hooks is not caught here. Codex, Gemini and other tools are not covered.
- The long-running `notify-daemon`, wake-ups of an idle parent (the nudge typed into the parent), `session send --wait`/`--stream`/`--queue`, `session children --follow`, `inbox peek`/`export`, remote (`remote drain`, SSH) and sandbox-provider flows: none of these run.
- Timing under load: each eval polls with a generous deadline (20-40 s), so a slow regression passes as long as it finishes in time.
- That a model follows the skill: these evals check the product's side of the contract, not whether an agent prints the sentinel or drains its inbox. The model-run task evals below cover what the skill tells the agent.
- The default tmux server and a user's own `~/.tmux.conf` are never touched, so behaviour without `socket_name` (the default for most users) is not exercised.

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
