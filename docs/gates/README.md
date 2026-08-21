# SIXGATE — "done" is not a claim, it is an artifact

This directory holds acceptance evidence, one subdirectory per feature.

## Why it exists

The context inspector was declared done on the strength of code analysis, a
2,961-transcript corpus replay and an adversarial review. **Nobody ever pressed
the key.** The first thing its user saw when he opened it was a blank
percentage he had to ask about.

Static verification cannot detect "this feels broken on arrival". Only a
recording of the software being used can. So the rule is:

> **No transcript, not done.** A green unit test beside a blank on-screen
> percentage is the exact failure this framework exists to prevent.

## The six gates

| gate | what it demands | artifact |
|------|-----------------|----------|
| **G0 SCRIPT** | the journey written as literal keystrokes **before** building; if nobody can write it, the feature is undefined | `G0-script.yaml` |
| **G1 DRIVE** | something executes G0 end to end against the **real built artifact** and captures what a human would see | `G1-drive/<fixture>/NN-*.screen.txt`, `transcript.md`, `run.json` |
| **G2 ASSERT-ON-SCREEN** | assertions read the **rendered output**, never internals; plus the Blank Detector on every frame | `G2-assert/results.{json,md}` |
| **G3 MATRIX** | every tool type, every session state, empty and enormous inputs, and a **cold first run with no config** | `G3-matrix/matrix.{json,md}` |
| **G4 ORACLE** | every number compared against a truth **the author did not write**; any figure with no oracle must be labelled an estimate in the UI, permanently | `G4-oracle/parity.{json,md}` |
| **G5 COLD-EYE** | a reviewer given **only the binary and one sentence** reports their literal first three minutes | `G5-coldeye/{brief,report}.md` |

`VERDICT.md` (human) and `VERDICT.json` (machine) roll them up and bind the
result to a hash of every source file the feature owns.

## The three forcing functions

1. **Ordering.** `sixgate scaffold <slug>` writes `G0-script.yaml` and nothing
   else. It refuses to create `G1-drive/` … `G5-coldeye/` until G0 validates.
   You cannot start collecting evidence for a journey nobody wrote down.
2. **Expiry.** `sixgate verdict <slug> --check` re-hashes every owned source and
   re-renders `VERDICT.md` from `VERDICT.json`. A transcript recorded three
   commits ago, or a hand-edited verdict, stops counting as evidence.
   `done == verdict --check exits 0`.
3. **The Blank Detector.** Every frame in every gate is scanned for `( %)`,
   `NaN`, `<nil>`, `%!`, empty parentheses, a label with nothing after it,
   "8 items … 0 tokens", and friends — with no opt-in and nobody needing to know
   to look. Suppressing a rule requires a written justification inside the G0
   script. See `internal/sixgate/lint`.

## Using it

```sh
go build -o /tmp/sixgate ./tools/sixgate

/tmp/sixgate scaffold <slug> -sentence "..."   # writes G0 only
$EDITOR docs/gates/<slug>/G0-script.yaml       # write the journey, get it reviewed
/tmp/sixgate validate <slug>                   # schema + placeholder check
/tmp/sixgate scaffold <slug>                   # unlocks G1..G5 once G0 validates
/tmp/sixgate drive    <slug>                   # G1: run it, record every frame
/tmp/sixgate assert   <slug>                   # G2: judge those frames
/tmp/sixgate matrix   <slug>                   # G3: run it in every declared world
/tmp/sixgate verdict  <slug>                   # report: what evidence exists
/tmp/sixgate verdict  <slug> --check           # the gate: exit 0 means done
/tmp/sixgate selfcheck                         # gate the gates
```

Always under the sandbox preamble, which is not optional in this repository:

```sh
HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= /tmp/sixgate drive <slug>
```

## Drivers

`drive` executes the G0 journey. Which driver does it is the one project-specific
choice the framework makes; everything else in the tree is portable.

**Driver A — `teadrive` (default; no tmux, no PTY).** Boots the real `*ui.Home`
through the real Bubble Tea runtime and records `home.View()` per step. For a
full-screen Bubble Tea program `View()` *is* the string the terminal paints —
and the blank percentage lived in `View()`, so this driver alone would have
caught it (see `context-inspector/G2-assert/regression-proof/`, where the bug was
put back and the gate caught it). It is deterministic, sub-second, and creates no
pseudo-terminal; `run.json` records a PTY census proving it rather than promising
it. The seam is `internal/ui.NewGateHome`: a no-op `Init()` so the constructor's
background workers, storage watcher and tmux pipe manager never start.

What Driver A **cannot** see, stated here rather than discovered later: that the
shipped binary boots, real-terminal wrapping at odd widths, ANSI bleed,
alt-screen entry and exit, mouse input. Those need Driver B — a real binary in a
pane on a dedicated `-L ctxgate-<runid>` socket — which is where every tmux
safety rule in this repository applies. Where both drivers run the same script on
the same fixture, a differing frame is free evidence.

## The matrix (G3) declares its worlds, and its gaps

`G3-matrix/matrix.yaml` lists every world the journey has to survive. The `axes`
block fixes the vocabulary — a row naming a value no axis declares is a schema
error — and the rows are then listed one by one, each with a note saying what it
is for. Everything the matrix does **not** cover is listed too, as an exclusion
with a written reason, so a gap appears in `matrix.md` instead of being
invisible.

**Two coordinates cannot be excluded, and cannot be quietly omitted either:**
`config: cold-first-run` (a machine that has never run the software, which is
where first-run modals and empty states live) and `driver: B` (a real-binary row,
without which nothing in the gate proves the shipped artifact boots). Excluding
either is refused at the declaration; deleting the row instead is refused by
`include` validation. `matrix.SelfTest()` proves both, and `sixgate selfcheck`
runs it.

**A row may declare where it expects the journey to stop holding.** Most worlds
legitimately change what the screen can say — a session with no memory files
cannot show a memory-files category — and forcing every row to satisfy G0 would
leave two options, both bad: drop the row, or weaken the journey's assertions
until the hardest world passes, which is how a blank percentage ships. So a row
writes down the step and the reason, and the runner asserts the divergence
happens **exactly** there. That is stricter than passing: a row that starts
failing one step earlier fails the gate, and a declaration the software has
outgrown fails too.

```yaml
- id: claude-cold-narrow
  note: "the same session on an 80x24 terminal — the width most people actually have"
  fixture: claude-cold
  adapter: claude
  session_state: running
  data_size: typical
  config: configured
  terminal: "80x24"
  driver: A
  expect: diverges
  diverges_at: 08-fat-item
  why: "at 80 columns the drill's breadcrumb is the first thing the header clips …"
```

The one thing a row can never declare away is the Blank Detector: every frame of
every row is scanned — including the first-run modals no scripted step names —
and a finding fails the row whatever the row expected. A world is allowed to look
different; it is not allowed to look blank.

**Wall time is capped, and a row that exceeds its cap is never killed.** Both
drivers hold internally bounded waits and a deferred teardown that must prove its
tmux server is gone; terminating one mid-flight is exactly how this machine
leaked ~50 tmux servers and 507 of its 511 pseudo-terminals. So the cap stops the
row *counting* as running, the runner then waits for it to unwind, and the row is
recorded as `timeout` — never as a skip. If it does not unwind within
`budget.unwind_grace`, the run stops, the remaining rows are recorded as
`not-run`, and `leak_risk` is raised on the report for a pane row.

## Blank Detector suppressions are scoped

A suppression may name the capture steps it covers:

```yaml
banned_screen_patterns_allow:
  - pattern: orphan-percent
    steps: [01-home, 02-busy, 11-close]
    justification: "the bare % on the session list is the filter hotkey, not a figure that failed to render"
```

This exists because the first real run demanded it. `orphan-percent` fired on the
session list's keyboard legend, where `%` is the key you press — and silencing it
journey-wide would have disarmed the rule on the pager frames, which is the exact
screen the blank percentage shipped on. An unscoped suppression is still allowed;
it just has to be a deliberate choice rather than the only option.

`sixgate` is a standalone binary, not a `go test` harness, on purpose: gates run
against the artifact a user actually gets, on any machine, with no test runner.
In this repository an unsandboxed `go test` has destroyed live profile data
three times, so the gates must not need one.

## Universal vs. project-specific

Portable to any project, unchanged: the six-gate contract, the artifact tree,
the G0 schema and its verbs, the Blank Detector, G2's rule that the assert
runner cannot import product code, the matrix schema with its mandatory
cold-first-run row, the oracle schema with its *unoracled ⇒ must-be-labelled*
coupling, and the cold-eye protocol.

Swappable per project: the **driver** (a bubbletea in-process driver and a real
binary in a dedicated tmux socket here; Playwright for a web app, pty-expect for
a CLI, Maestro for mobile), the **fixture materializer**, and the **oracle
source** (Claude Code's own `/context` here).
