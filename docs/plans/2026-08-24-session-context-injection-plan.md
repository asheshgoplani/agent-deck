# SESSION CONTEXT INJECTION — PLAN

Target: agent-deck v1.16.0. Branch `feat/session-context-injection` off `github/main` (v1.15.0).
Written 2026-08-24 after a four-way code survey of `trunk` (launch mechanics, session facts, repo conventions, gauntlet harness). Line refs are `github/main`-era; structures verified to exist there.

## 0. Problem (evidence, not theory)

- A conductor sub-agent ran a raw recursive grep over a 1.8GB transcript corpus and hit 31GB RSS because it did not know `agent-deck session search` exists.
- Workers could not tell created-vs-resumed, so they redid pushed work or stopped short.
- Ashesh hand-explains agent-deck in every prompt.

The fix: every session gets a small, honest, runtime-derived context primer, on every harness, surviving resume.

## 1. Exact primer content

All facts come from the runtime `session.Instance` + two live probes (`git` branch re-probe, location resolution). A fact that cannot be determined prints `unknown` — never guessed, never omitted. "none" (no parent) and "harness default" (no explicit model) are facts, not unknowns.

Renderer contract (enforced by tests, not convention):
- `primer` level: ≤ 16 lines, ≤ 1100 chars (~275 tokens).
- `full` level: ≤ 26 lines, ≤ 1900 chars (~475 tokens).
- Wrapped in `<agent-deck-context>…</agent-deck-context>` so it is greppable and the eval can assert presence/absence byte-exactly.

### Worked example — fix worker, fresh launch

```
<agent-deck-context>
You run inside agent-deck (tmux session manager for AI coding agents); the `agent-deck` CLI is on PATH.
Session: 1f9b45f2 "fix-auth-timeout" | group: projects/backend | lifecycle: created (fresh conversation)
Dir: /home/ashesh/w/backend-wt-auth (git worktree of /home/ashesh/w/backend, branch fix/auth-timeout) | host: local
Harness: claude | model: claude-sonnet-5 | account: seminno | profile: default
Parent: 9c02d1a7 "nightly-conductor". Report results: agent-deck session send 9c02d1a7 "<message>"
Cheap paths — use these, never the raw alternative:
  agent-deck status --json                     # fleet summary (NOT `list --json`)
  agent-deck session search "<query>"          # indexed transcript search (NEVER recursive grep over $HOME or transcript dirs)
  agent-deck session children --follow --until-done   # wait on children (NOT a poll loop)
  agent-deck session output <id>   |   agent-deck session send <id> "<msg>"
Current fact sheet anytime: agent-deck session primer --json
</agent-deck-context>
```

### Worked example — orchestrator (`full`)

`full` = the primer above plus:

```
Orchestrator extras:
  agent-deck launch <path> -c <tool> -m "<task>" --json    # spawn a child (parent link is automatic)
  agent-deck session children --follow --until-done        # blocks until all children signal done
  Children signal done by printing: AGENTDECK DONE status=<ok|fail> summary="..."
  Respect group concurrency caps; check agent-deck status --json before mass-launching.
  Deep guide: the agent-deck skill (skills/agent-deck/SKILL.md) if installed.
```

### Worked example — RESUMED session

Identity lines identical, lifecycle line replaced by:

```
Lifecycle: resumed — this conversation existed before this launch. Do NOT assume prior steps are undone
or redo them: verify with `git log --oneline -5`, `git status`, and `agent-deck session output <own-id>`
before repeating any work. If you already pushed/reported, say so and stop.
```

A revived session (control-pipe heal of an errored session) prints `lifecycle: revived (process was restored after an error; verify state before continuing)`.

### Unknown rendering

`model: unknown` (e.g. copilot — `LaunchModelID()` has no copilot arm), `branch: unknown` (git probe fails), `host: unknown` (SSH host field empty on a remote-marked row). Standing honesty rule: `unknown` is printed, never guessed, never dropped.

### Fact sources (with the three documented truth-traps avoided)

| Fact | Source | Trap avoided |
|---|---|---|
| id, title, group | `Instance.ID/Title/GroupPath` | auto-name sessions: append saved description when present |
| dir + host | `session.LocationOf(inst)` | `ProjectPath` is a local placeholder for SSH sessions (#1850-53) — never used directly |
| worktree + branch | `IsWorktree()` + live `git.GetCurrentBranch(cwd)` | `WorktreeBranch` is a creation-time snapshot; re-probe, `unknown` on failure |
| harness, model | `Tool`, `LaunchModelInfo()` | empty model = "harness default", copilot = `unknown` |
| account, profile | `Account` (+resolution), effective profile | — |
| parent | `ParentSessionID` + parent title lookup | absent = "none (top-level session)" |
| lifecycle | passed by the call site (fresh-start builder vs resume builder vs reviver) | never inferred from conversation-id presence alone (#1815 identity guard) |

## 2. Capability lines

Exactly five commands in `primer` (the cheap-path set): `status --json`, `session search`, `session children --follow --until-done`, `session output`, `session send`; plus the self-referential `session primer --json`. `full` adds `launch` and the done-sentinel line. Nothing else. Additions require deleting a line or raising the budget test in the same PR (see §6).

## 3. Delivery per harness

Two-part delivery everywhere:

- **Env facts (universal spine):** `buildEnvSourceCommand()` (`internal/session/env.go:28`) is the one choke point already executed by every fresh-start builder AND every resume builder on all 11 harnesses, and survives `bash -c`/SSH/sandbox wrapping (host tmux env does not reach docker — `collectDockerEnvVars`). Add one layer exporting `AGENTDECK_SESSION_ID`, `AGENTDECK_TITLE`, `AGENTDECK_TOOL`, `AGENTDECK_GROUP`, `AGENTDECK_PARENT_ID`, `AGENTDECK_LIFECYCLE`, `AGENTDECK_CONTEXT_LEVEL` (level `none` ⇒ no primer text anywhere, but the two identity vars `AGENTDECK_SESSION_ID`/`AGENTDECK_PROFILE` that predate this feature keep their existing behavior).
- **Visible primer text** via the strongest NATIVE mechanism per harness:

| Harness | Native options | Chosen | Survives resume? |
|---|---|---|---|
| claude | cwd `CLAUDE.md` (pollutes user repo); `--append-system-prompt`; **SessionStart hook `additionalContext`** (agent-deck already installs hooks; `hook_children_context.go` precedent) | **SYNCHRONOUS SessionStart hook** — Claude Code only reads hook stdout (additionalContext) from sync hooks, so the install flips SessionStart async→sync and every claude spawn ensures/upgrades the hooks in its config dir (round-2 P1: under async, delivery silently no-oped on default installs); `agent-deck hook-handler` computes facts live at fire time; fires on startup AND resume, so the resumed primer says `resumed` with zero staleness | **YES — native (verified by the no-tools gate cell)** |
| gemini | cwd `GEMINI.md`; agent-deck gemini-hooks (SessionStart/BeforeAgent) | hook injection **if** the gemini hook protocol accepts injected context (verified at build time); else initial-message prepend | claimed only after verification; else PARTIAL |
| codex | `AGENTS.md` in cwd (pollutes repo) or `$CODEX_HOME` (shared across sessions of an account — per-session facts would collide); argv prompt (fresh only); notify hook is outbound-only | **initial-message/argv prepend on fresh** + env spine | **PARTIAL — stated plainly:** on resume only env vars refresh; the fresh primer remains in conversation history but its lifecycle line is stale. No native re-injection path exists today. |
| opencode | `AGENTS.md` (same pollution/collision) | initial-message prepend + env spine | **PARTIAL** (same as codex) |
| dsh/deepseek | prompt delivery is profile-classified (command-line / pane / unsupported); the headless one-shot task is REPLAYED VERBATIM on restart | **env spine only** — an embedded primer would replay stale lifecycle facts on restart (discovered by the existing headless-lifecycle test during build); web profile refuses prompts anyway | env YES; visible primer NO — stated plainly |
| cursor-agent | `.cursorrules` (pollutes repo); cursor hooks exist (injection support unverified) | initial-message prepend + env spine | **PARTIAL** |
| hermes | `HERMES.md`; hermes hooks | initial-message prepend + env spine (already carries inline AGENTDECK_*) | **PARTIAL** |
| omp | not in tree yet | documented: adopts the generic path on merge | n/a |
| generic `[tools.X]` | none knowable | inline env spine (buildGenericCommand flows through buildEnvSourceCommand); prepend when a launch message is given | env YES; visibility weak — stated plainly |
| plain `--cmd` shell / raw command | none — the command is typed via send-keys into the user's interactive login shell (possibly fish), so inline `export` is forbidden (#1821) | **host-side tmux session environment** (`tmux show-environment`, every later pane/window) — NOT the already-running initial shell's process env; scripts run `agent-deck session primer` | tmux env YES (spawn + restart); initial-shell env NO — stated plainly |
| shell (default, no command) | n/a (human/scripts) | host-side tmux session environment, same as plain `--cmd` | tmux env YES |

Prepend mechanics: in `StartWithMessage`/launch, `message = primer + "\n\n" + message` when level ≠ none and the harness has no hook path. A session started with no message on a no-hook harness gets env spine only — reported honestly in the matrix, not papered over.

**Degradation rule (hard):** any error collecting facts or rendering ⇒ log at debug, inject nothing, launch proceeds. Injection can never fail a launch. Harnesses that ignore unknown env vars are unaffected (exports are plain `export K=V` prefix, same mechanism as today's 9 env layers).

## 4. Levels

`none` / `primer` / `full`; default `primer`.

Resolution (most-specific wins): per-session `context-level` (CLI flag `--context-level` on add/launch, `session set <id> context-level`, TUI edit dialog) → group `[groups."<path>"].context_level` (nearest-ancestor walk, same engine as group `account`) → global `context_level` in config.toml → built-in default: `full` when `IsConductor`, else `primer`. Standing ruling honored: orchestrators get `full`; workers get `primer` — the orchestrator skill is never forced onto workers.

## 5. Cost model (acceptance criterion: saved > added)

Added per session: primer ≈ 275 tokens injected once per SessionStart (claude: startup + each resume; others: once per fresh launch). A session resumed 5× costs ≈ 1.6k tokens of primer, ~0.001% of a typical 1M+ token worker session.

Saved, per single adoption event (measured shapes from this repo's own history):
- `status --json` (≈40 tokens) vs `list --json` on a 62-session deck (≈6–15k tokens): **~10k tokens per status check**; conductors poll status dozens of times per shift.
- `session search` vs recursive grep over transcripts: the observed failure was a 31GB near-freeze — bounded replacement ≈ 200 tokens vs an unbounded tool-output flood (tens of thousands of tokens before the crash).
- `children --follow --until-done` vs a poll loop: eliminates N poll turns × (command + output) ≈ 500–2000 tokens per wait, plus wall-clock.
- Resumed-state honesty: one avoided redo of an already-pushed fix ≈ an entire duplicated work session.

Break-even: one `status --json` adoption pays for ~36 primer injections. The eval records actual tokens per matrix cell; if measured savings do not exceed measured additions, the primer shrinks until it pays or the recommendation is to drop the feature.

## 6. Risks

- **Drift as the CLI changes:** the repo's "never-stale-skill" gate is informal today (watcher-scoped tests only). This feature adds a compiled drift test: `TestPrimerCommandsExist` asserts every `agent-deck` invocation string in the primer template resolves against the real command dispatch (and the eval judge re-checks at gauntlet time). Registered in `.claude/release-tests.yaml`.
- **Dumping ground:** hard line/char budget enforced by `TestPrimerBudget` (fails the build, not a review comment). Adding a line means removing one or justifying a budget bump in the same diff.
- **Naming collision:** `agent-deck session context` already exists (context-window inspector). The new command is `agent-deck session primer`.

## 7. Build order (Part 2)

1. Worktree `feat/session-context-injection` off `github/main`.
2. `internal/session/primer.go` + tests: `PrimerFacts` collection (all traps), `RenderPrimer(facts, level, lifecycle)`, budget test, unknown-rendering table test.
3. `ContextLevel` field: Instance + `mutators.go` registry (+membership test, restart-required), group/global config resolution + tests.
4. Env spine in `env.go` + command-assembly tests: exports present on fresh AND resume builders; `none` ⇒ no primer text; string assertions only, no harness spawned.
5. Claude hook: primer in SessionStart `additionalContext` (piggyback `hook_children_context.go` wiring) + handler test; gemini hook if protocol supports, else prepend.
6. Message-prepend path for no-hook harnesses + per-harness gating (deepseek delivery classes) + tests.
7. CLI: `session primer [id] [--json]`, `--context-level` flags, `session set` field; TUI: edit-session dialog row.
8. Red-path tests: fresh primer present; still present after resume (claude hook path + env spine on all); `none` injects nothing; unknown fields render `unknown`.
9. Docs in same PR: SKILL.md, references/cli-reference.md (incl. the stale Fields list), references/config-reference.md, README, CHANGELOG Unreleased, release-tests.yaml.
10. Build + `go test` **in container only** (golang image, isolated HOME/XDG — repo's own env-isolation idiom, inside docker). Never on this host.
11. Commit signed "Committed by Ashesh Goplani" (no AI attribution), push, open PR to `asheshgoplani/agent-deck` main. **Do not merge.**

## 8. Gauntlet matrix eval (Part 3 — gates the merge)

Reuse the gauntlet isolation pattern (`overnight/gauntlet/capture.sh` + `onboarding-capture.sh`): throwaway HOME under /tmp, all `XDG_*` cleared, private tmux socket per cell, `TMUX_TMPDIR` in the throwaway, EXIT-trap server kill. Everything inside docker (the trunk `sandbox/Dockerfile` image carries claude/codex/gemini/opencode CLIs; credentials by bind-mounted config dirs, never env keys — same as the product sandbox and `judge.sh`'s `CODEX_HOME` pattern).

Matrix: harness × {none, primer, full} × {fresh, resumed}. Harnesses actually runnable = those with working in-container credentials (claude confirmed; codex via a `CODEX_HOME` profile; gemini/opencode if creds exist). A cell that cannot run is published as `could-not-verify` — never averaged away, never silently passed (evals honesty rule).

Per cell: launch a real session, ask the four questions (Who are you? Where are you — dir/branch/host? Who do you report to and how? Did you already start this work?), then a two-task set: (T1) "find which session mentioned X" — cheap path = `session search`, failure = grep; (T2) "wait for your child / check your children" — cheap path = `--follow --until-done`, failure = poll loop. Resumed cells get a pre-seeded first task before restart so Q4 has a true answer.

Scoring: LLM judge (one-shot `codex exec`, read-only, persisted request+verdict — `judge.sh` pattern, with the verdict lines explicitly requested, copying `onboarding-judge.sh`'s wording since `judge.sh` forgot to and scored 0). Machine lines:
`CELL_VERDICT: <harness>/<level>/<state> Q<n> CORRECT|GUESSED|UNKNOWN_HONEST` and `TASK_VERDICT: … T<n> CHEAP|EXPENSIVE|FAILED`. Absence of a line = failure. `GUESSED` is the failure mode that matters: each guessed cell must map to a specific missing/weak primer line, and that mapping is the feedback loop that keeps the primer short instead of long.

Output: MATRIX-RESULTS.md — full per-cell table (no averaging), tokens added/saved per cell, the guessed→primer-line map, and a plain statement of which harnesses cannot carry context across a resume.
