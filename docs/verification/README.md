# Capability Verification

This directory holds the **user-level capability verification** artifacts for agent-deck:
the checklist that tracks whether each capability in the
[v2 Capability Spec](../../) (229 stable `CAP-<AREA>-NNN` IDs) has been verified
**as a user would experience it** — by driving the real binary on each surface it
supports and capturing visual evidence — not merely by unit tests.

## Files

- **`CAPABILITY-CHECKLIST.md`** — human-readable matrix: capability × surface, with
  status (`verified` / `broken` / `partial` / `untestable-locally` / `deferred` /
  `pending`), the date it was user-verified, and a link to the evidence.
- **`CAPABILITY-CHECKLIST.json`** — the same matrix, machine-trackable, so the next
  verification run can resume exactly where the last left off.

## Verification philosophy

A passing `go test` proves a *function* works. It does not prove the *product* works
for a user. This checklist owns the second claim. A row is only marked `verified` when
the binary was actually **driven** — real CLI commands, real TUI keystrokes, real web
clicks — and the resulting behavior was **visually evidenced**:

| Surface | How it's driven as a user | Evidence form |
|---|---|---|
| **CLI** | real subcommand in a sandbox; assert output shape, `--json` keys, exit codes, persistence side-effects | command transcript (`.txt`) |
| **TUI** | the real binary run in a tmux pane, driven with `send-keys`, captured with `capture-pane` | terminal snapshot (`.txt`) |
| **WEB** | headless `agent-deck web --no-tui` (or the in-memory fixture) driven via browser + REST | screenshot (`.png`) + REST JSON |
| **daemon / remote / LLM** | no safe local user path → `untestable-locally` with a stated reason | — |

## Safety

Every verification run executes in a **fully sandboxed environment** — throwaway
`HOME`, sandboxed `XDG_*`, and an isolated `tmux -L <socket>` server inside a sandboxed
`TMUX_TMPDIR` — so it can never touch a real `~/.agent-deck`, the real `$HOME`, or the
user's tmux server. Sessions under test use a `bash` stub tool; no real LLM is ever
invoked. The repeatable method (bootstrap script + per-surface recipes + orchestration)
is packaged as the `capability-verification` skill.

## Re-running

Regenerate the skeleton from the spec and fold in new wave results with the skill's
scripts (`gen_checklist.py`, `merge_results.py`). The checklist is append-and-update:
`pending` / `deferred` rows are what the next run picks up.
