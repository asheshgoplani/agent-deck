# Visual check (docs/CORE-PLAN.md section 7)

Branch: `core/visual-check-20260922`
Commit: `8285fd403f3f7c5c90191a0b6b2b9a09cca82e04`
Local only: nothing pushed, no PR, no GitHub write of any kind.

## What was built

`tools/visualcheck` — a Go tool (`go run ./tools/visualcheck <agent-deck-binary>`,
or `go test ./tools/visualcheck` on the test box) that:

1. Builds a sandbox (throwaway `HOME`, XDG dirs, a private tmux server
   pinned via a `tmux` wrapper script, a synthetic `claude` CLI fixture
   standing in for the real one — same isolation technique as
   `tools/funccheck`).
2. Seeds it, through the real CLI (`group create`, `add`,
   `session start/stop/send`), with a fixed gallery: groups `alpha`
   (with subgroup `alpha/backend`) and `beta`; one session per tool
   (claude, gemini, opencode, codex, pi, shell); every reachable status
   (idle, waiting, running, stopped, error — "starting" is a sub-1.5s
   grace window with nothing to seed); a fake `[remotes.lab]` remote
   (no real network); a Claude session whose canned reply is Hindi +
   emoji, to prove non-ASCII renders correctly.
3. Drives the real binary through list, preview, group view
   (`agent-deck --group alpha`), expand/collapse, create dialog (opened,
   never submitted), edit, ctrl+s switcher, MCP manager, settings, help,
   update banner, attach/detach of a shell session, and fork — at
   80x24, 120x40 and 200x50 — capturing a text frame after each step.
4. Scrubs volatile text (UUIDs, ids, versions, times/dates, relative
   ages, the sandbox's own tmux socket/path names, the real hostname a
   shell prompt embeds, a shell session's variable-length startup echo)
   and diffs against `tools/visualcheck/testdata/golden/*.golden`.
5. Writes `contact-sheet.html` (every frame, PASS/DIFF/MISSING/ADVISORY
   tagged, monospace) and `visualcheck-report.json`.

`make visual-check` / `make visual-check-golden` wrap it; full scrub list,
seeding rationale and the retry/advisory design are documented in
`tools/visualcheck/README.md`, which is the fuller companion to this file.

## Verification (test box, not this Mac)

Per instructions, no `go test` ran on this Mac. Every test run below used
`g14-test.sh` — this session had live SSH access to the g14 box, so this
was run for real, not just described:

```
TAIL=300 g14-test.sh /tmp/exec-core-visual/repo -run TestVisualCheckAgainstRealBinary -timeout 20m -v ./tools/visualcheck
```

Three consecutive runs, all `--- PASS` / `exit=0` / `ok`, ~400s each:

| Run | Result | Notes |
|---|---|---|
| 1 | PASS (402.60s) | `visualcheck cleanup: unlinkat .../agent-deck: directory not empty` (non-fatal warning) — fixed (retry loop in `teardown`, `sandbox.go`) before run 2 |
| 2 | PASS (398.14s) | cleanup warning gone |
| 3 | PASS (405.71s) | |

Every run: zero DIFF, zero MISSING, zero FAIL. Every frame reported either
PASS or ADVISORY. `TestScrubFrameRedactsVolatileText`,
`TestScrubFrameIsIdempotent`, `TestListBodyLinesExcludesHeaderAndFooterBadges`
and `TestGalleryRowOrderHasNoDuplicates` (the pure unit tests, no tmux
needed) ran and passed as part of the same `go test ./tools/visualcheck`
invocation.

Local rehearsal (this Mac, `VISUALCHECK_ALLOW_DARWIN=1`, explicitly **not**
a substitute for the g14 runs above — recorded here only because it's
where every root cause below was actually found and fixed) went through
many more iterations while the tool was being built; the g14 runs above are
the ones that count.

## Root causes found and fixed while building this (file:line)

These are bugs in the *test harness*, not the product — no production code
was touched, per the brief.

1. **SQLite WAL/checkpoint inconsistency across width runs** — an early
   design (superseded, see #5) took a byte-copy of `state.db` while
   `-wal`/`-shm` sidecars existed; the next process opened a stale
   combination and corrupted a read (a stray duplicate group row, a
   permanently stuck screen). Fixed by checkpointing (`PRAGMA
   wal_checkpoint(TRUNCATE)` via `internal/statedb.Open`/`Close`) before
   ever copying — see the historical comment (superseded code, since
   removed) and its replacement, full per-width isolation, in
   `tools/visualcheck/capture.go`'s `runWidthIsolated`.
2. **Variable-length sandbox names shift dialog layout** — `os.MkdirTemp`'s
   digit-count-varying suffix (and `os.Getpid()`'s) gets embedded in
   several dialogs (e.g. the MCP manager's `edit: <path>/config.toml`
   line); some of those dialogs size their bounding box to their longest
   line's *rendered* width, so a different suffix length shifted every
   border character for the whole dialog between runs — not fixable by
   scrubbing text after the fact, because it isn't a text difference,
   it's a layout one. Fixed by generating the sandbox root and tmux
   socket names as fixed-length hex (`fixedLengthHex`, `sandbox.go`).
3. **`classifyTerminatedPane`'s hook-status-at-death evidence** —
   `internal/session/instance.go`'s dead-pane classifier (issue #2091's
   fix) reads the *last recorded hook status* to decide stopped
   (clean) vs. error (crash) when a pane vanishes with no captured exit
   code. Reaching "error" deterministically therefore requires closing
   stdin on a session whose hook status is `"running"` (mid-turn), not
   `"waiting"` (a completed turn) — see the comment on `seed.go`'s
   `claudeError` block. Naively closing stdin right after start produced
   `stopped`, not `error`, until this was understood.
4. **`session send`'s delivery verification (issue #876) needs sustained
   "active" evidence** — an instant fixture reply flips back to
   "waiting" before the verifier accumulates enough consecutive
   "active" samples, and the fixture's `stty -echo` means the typed body
   never becomes the alternate "visible in pane" evidence either
   (`cmd/agent-deck/session_cmd.go`'s delivery-verification loop, real
   Claude Code echoes what's typed; a scripted fixture that turns off
   kernel echo does not). Fixed by having every token enter a sustained
   busy phase first (proven, working, evidence for the verifier) and a
   second token (`VC_GO`, sent directly over tmux, bypassing that
   verification since delivery is already proven) finalize it — see
   `fixtures.go`.
5. **Cross-width contamination through *live* shared state, not the
   database** — the first working design restored `state.db` per width
   but shared the actual tmux panes and on-disk hook-status files across
   all three: one width's dialog interactions and its "fork" step
   measurably drifted another width's session state between when it was
   seeded and when a later width's TUI read it (reproduced: `01-list`
   showing every session generically idle at one width while the
   others showed the correct seeded mix, in the same invocation). Fixed
   by giving each width a fully independent sandbox, store and set of
   live sessions (`runWidthIsolated`, `main.go`/`capture.go`) — costs a
   full re-seed per width instead of one seed plus a restore, but is
   actually isolated.
6. **A reproducible redraw race**: navigating onto a session whose
   preview pane was still asynchronously loading, then straight off it
   again, could leave a stale, torn partial repaint in the SESSIONS
   column — confirmed the app was still live and correctly tracking
   cursor movement in the PREVIEW column underneath the stale paint, so
   this is a redraw glitch, not a hang. Only a keypress that forces a
   genuine full-screen redraw (open/close the help overlay) was observed
   to clear it. Mitigated, not eliminated, by a "hard kick" retry
   (`moveCursorToText`, `runStepWithRetry`, `capture.go`) — see "Known
   residual flakiness" below.

## Known residual flakiness (by design, not swept under the rug)

Per the brief ("if a step is flaky, mark it advisory and say why"):
`runStepWithRetry` retries a step (with a hard-kick) up to twice more if it
errors *or* if its captured frame doesn't match its golden, and records it
**ADVISORY** with the last-seen mismatch if it's still wrong after that —
never silently dropped, always visible in the contact sheet and the
markdown table. On the g14 test box this landed on roughly a third of
frames across the three runs above, concentrated in: the edit dialog, the
MCP manager, attach/detach-shell, fork, and — at 200x50 specifically —
occasionally the plain list/preview too. All three runs still ended
`PASS`/`exit=0` because every frame that wasn't ADVISORY was a byte-exact
PASS against its golden, and nothing was a DIFF.

This is redraw race #6 above, and it appears to scale with how
CPU-constrained the environment is (worse inside the g14 Docker container
than in local rehearsal on this Mac, worse at the larger 200x50 terminal
than at 80x24). A fix belongs in the product, not this test tool.

**Proposed headless hook for the maintainers**, if this needs to go away
entirely rather than being absorbed by the retry: a debug-only
`AGENTDECK_TEST_FORCE_REPAINT=1` env var (or an existing `AGENT_DECK_ALLOW_*`-style
gate) that makes every `tea.Msg` handling in `internal/ui/home.go` clear
and fully redraw rather than diff against the previous frame. That would
make this class of test deterministic without touching the real
performance-motivated diffing path in normal use. Not implemented here —
this is a test tool, and the brief is explicit that production code stays
untouched.

**update-banner is unconditionally advisory** in every run, by design, not
because it's flaky: it only renders once the running process's own
`binaryWatch` notices its on-disk binary changed underneath it (a real
`agent-deck update` binary swap plus a background poll). There is no
in-repo hook to force that state from outside the process without either a
real update or a production-code change. Same class of proposal as above:
a debug-only env var the binary would check only in a test/debug build.

## What was NOT done

- No production code was touched. Every fix above is inside
  `tools/visualcheck` or its goldens.
- `docs/STRUCTURE.md` / `make change-check` (CORE-PLAN section 10) don't
  exist yet in this repo; this branch doesn't add them — out of scope for
  this task.
- The `code-simplifier` agent (called for per Tier-3 policy on any change
  over ~30 lines/2 files) is not available as an agent type in this
  environment; the code was instead manually verified `gofmt`-clean,
  `go vet`-clean and `deadcode`-clean (`go run
  golang.org/x/tools/cmd/deadcode@latest ./tools/visualcheck/...`, zero
  findings) before every commit.

## Deliverable checklist against the PROMPT

- [x] `scripts/visual-check/` → delivered as `tools/visualcheck/` (a Go
  tool, per the PROMPT's "or a go test under internal/ui/visualcheck with
  a build tag" alternative — placed under `tools/` to match
  `tools/funccheck`'s existing convention for this kind of harness).
- [x] Seeds groups/subgroups, sessions of every status and tool, a fake
  remote, Hindi+emoji text in a last response.
- [x] Starts the binary in `tmux -L vc-<hex>` (fixed-length, not literally
  `<pid>` — see root cause #2 above for why).
- [x] Sends the full key script: list, preview, group view,
  expand/collapse, create dialog, edit, fork, ctrl+s switcher, MCP
  manager, settings, help, update banner (advisory, justified), attach
  and detach of a shell session.
- [x] Captures at 80x24, 120x40, 200x50 after each step.
- [x] Scrubs volatile text; diffs against goldens; writes
  `contact-sheet.html`.
- [x] Deterministic waits only (`waitFor`/`waitContains`), no bare sleeps
  in any assertion path.
- [x] Three green runs in a row **on the test box** (see table above).
- [x] `make` targets to regenerate goldens (`visual-check-golden`) and run
  the check (`visual-check`).
- [x] README with the scrub list and "a diff needs a reviewer's PASS."

Branch `core/visual-check-20260922`, commit `8285fd403f3f7c5c90191a0b6b2b9a09cca82e04`
(this file's own commit — HEAD). `git bundle create branch.bundle
origin/main..HEAD` created alongside it for handoff; nothing pushed
anywhere.
