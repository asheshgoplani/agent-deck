# visualcheck

The visual check from `docs/CORE-PLAN.md` section 7: one command that drives
the real `agent-deck` binary through every screen and dialog in a private
tmux server, against a sandboxed HOME and a seeded store, captures text
frames at 80x24 / 120x40 / 200x50 after each step, diffs them against
committed goldens, and writes an HTML contact sheet for a two-minute human
look.

## Running it

```
make build
make visual-check                  # builds ./build/agent-deck, runs the check against it
make visual-check-golden           # same, but (re)writes testdata/golden/* instead of diffing
```

Or directly:

```
go run ./tools/visualcheck ./build/agent-deck
```

It refuses to run on macOS by default (`visualcheck drives a Linux
tmux/hook stack; run it on the g14 test box`) — same policy as
`tools/funccheck`, because the hook-status classification paths this
exercises are Linux production behavior. Set `VISUALCHECK_ALLOW_DARWIN=1` to
force a local dry run while developing the check itself; that is a
development escape hatch, not how it is meant to be scored.

On the g14 test box, via `g14-test.sh`:

```
VISUALCHECK_BINARY=/path/to/agent-deck g14-test.sh <repo-dir> -run TestVisualCheckAgainstRealBinary ./tools/visualcheck
```

`TestVisualCheckAgainstRealBinary` only runs when `VISUALCHECK_BINARY` is
set — a bare `go test ./tools/visualcheck` (with no binary given) only runs
the pure unit tests (scrub rules, frame-boundary detection), which need
neither Linux nor tmux.

Outputs, written to the working directory: `contact-sheet.html` (every
frame, PASS/DIFF/MISSING/ADVISORY tagged) and `visualcheck-report.json`
(machine-readable). Exit code is non-zero on any DIFF, MISSING or run
failure; ADVISORY frames never fail the run.

## What it seeds

`seed.go` drives the real CLI (`group create`, `add`, `session
start/stop/send`) to build a fixed gallery in a fresh sandbox `HOME`:

- Groups: `alpha` (root), `alpha/backend` (subgroup), `beta` (root).
- One session per tool (claude, gemini, opencode, codex, pi, shell).
- Every reachable session status:
  - **idle** — `add`ed, never started (the natural default).
  - **waiting** — started; the fixture's hooks fire SessionStart then Stop
    before printing its prompt.
  - **running** — started, sent a token the fixture answers by staying
    busy (printing "esc to interrupt" and blocking on the next line) —
    this is the one status this tool waits on real, sustained hook-driven
    state for, not a shortcut.
  - **stopped** — started, then `session stop`.
  - **error** — started, sent a busy token (so its last recorded hook
    status is "running", not "waiting"), then its stdin is closed under
    it. `internal/session/instance.go`'s `classifyTerminatedPane` reads
    that hook-status-at-death evidence to tell a mid-turn crash from a
    clean completion racing the pane's exit — see the comment on
    `seed.go`'s `claudeError` block for the full citation. This reaches
    "error" through the same evidence path a real crash does, not a
    hand-set status column.
  - **starting** is not reachable: it is a sub-1.5-second grace window
    (`internal/session/instance.go`'s `updateStatus`) between spawn and
    the first status read, too narrow to land a `session show` poll on
    deterministically. Not attempted; not silently skipped either — it is
    simply not one of the five statuses in the gallery, and isn't listed
    as a step, so there's nothing to mark advisory.
- A fake remote (`[remotes.lab]` in `config.toml`, no real network) so the
  remote group/row renders (as unreachable — the test box runs
  `--network none`).
- A Claude session (`claude-i18n`) whose canned reply is Hindi + emoji
  (`नमस्ते दुनिया 🚀🌟 यह परीक्षण है।`), to prove non-ASCII renders correctly
  in the preview pane and the contact sheet.

Each width gets a fully independent sandbox: its own HOME, its own private
tmux server, its own seeded store, its own live sessions (`runWidthIsolated`
in `capture.go`) — not one seed reused, resized, three times. That costs a
full re-seed per width instead of one seed plus a database restore, but
rehearsal found the cheaper approach wasn't actually isolated: the store was
restored per width, but the *live* tmux panes and on-disk hook-status files
behind claude-waiting/claude-running/claude-error were the same physical
objects for all three widths, and one width's dialog interactions (or the
"fork" step, the one mutating step) measurably drifted another width's
session state between when it was seeded and when that later width's TUI
actually read it — a real, reproducible source of DIFFs a database snapshot
alone could never prevent, because the drift was never in the database.

## The key script

`steps.go`'s `visualCheckSteps`, in order: list, preview (Hindi/emoji
session selected), group view (`agent-deck --group alpha`, since
`SetGroupScope` is a launch flag, not a keypress the running TUI ever
reaches — see the comment on `stepGroupView`), expand/collapse, create
dialog (opened, never submitted), edit, ctrl+s switcher, MCP manager,
settings, help, update banner (advisory — see below), attach and detach of
a shell session, and finally fork (run last because it is the one step
that mutates the store, and every earlier step's frame has already been
captured against the unmutated list).

## Determinism

- Every wait is on frame content (`waitFor`/`waitContains`), never a bare
  `sleep`.
- Cursor navigation (`moveCursorToText` in `capture.go`) jumps to the top
  with `Home` and presses `Down` a fixed number of times computed from
  `galleryRowOrder` — the gallery's fixed, known insertion order — rather
  than a live binary search off the cursor's highlight color. A live
  search was tried first and found to occasionally land a keypress while
  the app was still mid-redraw (see below); arithmetic off a known order
  sends far fewer keys and is easier to reason about.
- **The one rendering race found during rehearsal**: navigating onto a
  session whose preview pane was still asynchronously loading, then
  straight off it again, was observed to leave a stale, torn partial
  repaint in the SESSIONS column — confirmed still-live and correctly
  tracking cursor movement in the PREVIEW column underneath the stale
  paint, so this is a redraw glitch, not a hang. Only a keypress that
  forces a genuine full-screen redraw (open and close the help overlay)
  was observed to clear it. `moveCursorToText` and `runStepWithRetry`
  (`capture.go`) both apply that "hard kick" periodically while polling.
  `runStepWithRetry` treats a captured frame that doesn't match its
  committed golden exactly the same way it treats a step that errored
  outright — as a reason to hard-kick and retry — because rehearsal showed
  the identical redraw race producing a *wrong-content* frame just as
  often as an outright timeout (a shell session's preview pane snapshotting
  its own scrollback mid-update, a session list caught between its
  cold-load default status and its settled one). A step that still isn't
  right after two hard-kicked retries is recorded **ADVISORY** with the
  last-seen mismatch/error, rather than failing the whole width run — this
  is the tool's one acknowledged source of flakiness; everything else is a
  hard failure. `RESULTS.md` proposes a headless hook for the maintainers
  to consider if this needs to go away entirely.
- **update-banner is unconditionally advisory.** It only renders once the
  running process's own `binaryWatch` notices its on-disk binary changed
  underneath it (i.e. a real `agent-deck update` swap plus a background
  poll). There is no in-repo hook to force that state from outside the
  process without either a real network update or a change to production
  code, and this is a test tool — see `RESULTS.md` for a proposed
  headless hook (an env var the binary would check only in a debug
  build/test harness).

## Scrub list

Applied in `scrub.go`'s `scrubRules`, in order, before every diff and
before every frame is written into the contact sheet:

| Rule | Matches | Replaced with |
|---|---|---|
| `uuid` | `xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx` | `<uuid>` |
| `agent-deck-id` | a bare 7-12 hex char id | `<id>` |
| `semver` | `v1.16.10`, `2.1.0-rc.1` | `<version>` |
| `clock-time` | `14:32:09`, `9:05` | `<time>` |
| `iso-date` | `2026-09-22`, `2026-09-22T14:32:09Z` | `<date>` |
| `relative-age` | `3m`, `2.5h`, `10s ago` | `<age>` (` ago` kept if present) |
| `just-now` | `just now` | `<age>` |
| `pid-socket` | `vc-1a2b3c4d` (our own tmux socket name) | `vc-<pid>` |
| `tmp-path` | `/tmp/vc-<hex>/...` through the next separator/ellipsis | `/tmp/vc-<tmp>` |
| `shell-prompt` | the attach-shell step's real system-shell prompt (embeds host + account name) | `<shell-prompt>$` |

The sandbox root and tmux socket names themselves are generated as a fixed
10 (root) / 8 (socket) hex characters (`fixedLengthHex` in `sandbox.go`),
not `os.MkdirTemp`'s variable-digit-count suffix or the PID: several
dialogs (e.g. the MCP manager's "edit: `<path>/config.toml`" line) size
their bounding box to their longest line's *rendered* width, so a
different suffix length between runs shifted that box's border column for
the whole dialog — a difference the `tmp-path` scrub rule alone could
never undo, because it isn't textual, it's layout.

## The rule

**A diff needs a reviewer's PASS.** `make visual-check-golden` regenerates
`testdata/golden/*.golden` from whatever the binary currently renders; it
does not, by itself, mean the new frames are correct. Every regeneration
in a PR must be looked at (the contact sheet is exactly the artifact for
this) and explicitly approved before it lands — a rubber-stamped
regeneration defeats the entire point of a visual check.
