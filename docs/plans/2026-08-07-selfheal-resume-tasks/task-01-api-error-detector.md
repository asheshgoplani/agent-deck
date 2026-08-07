# Task 01 — `api-error` detector, substate, ordering, glyph/label, `stalled` preservation

tier: mid
depends on: nothing
parallel with: nothing (it edits `internal/session/instance.go`, which task 04 also edits)
worktree: `/Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` (branch `feature/selfheal-auto-resume`)

Use absolute paths under that worktree for every Read/Edit/Write, and
`git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume` for
every git command. Never run `git stash`, `git checkout`, `git switch`, or
`git reset`; never edit the root checkout at `/Users/doozyx/DoozyX/agent-deck`.

---

## Design extracts (verbatim from the approved design)

> ### 1.1 Transport error
>
> Field evidence, 2026-08-07: a DNS failure wedged 3 of 32 live sessions for 16,
> 18 and 39 minutes. Each pane ended on:
>
> ```
> ⏺ API Error: Unable to connect to API (ENOTFOUND)
> ✻ Sautéed for 39m 27s
> ```
>
> The panes were not frozen — they repainted and accepted keystrokes. The network
> recovered long before anyone noticed. A single continuation prompt resumed all
> three on the first attempt (`delivery: submitted`, 3/3).
>
> `internal/tmux/detector.go` matches `API Error: 401`, `API Error (401`,
> `Please run /login` and `socket connection closed`. A transport error matches
> none of them, so all three sessions classified as `idle-at-empty-prompt`.

> ### D1 — New substate `api-error`
>
> Add `SubstateAPIError = "api-error"` to `internal/tmux/substate.go`, classified
> from the rendered banner. Markers: `Unable to connect to API`, `ENOTFOUND`,
> `ECONNREFUSED`, `ConnectionRefused`.
>
> Refactor `hasClaudeErrorBanner` to take a marker set, and drive both the
> existing `auth-401` check and the new one through it. This is load-bearing, not
> tidiness — the real banner renders on a `⏺` assistant line, which is precisely
> the line class `claudeAssistantLinePrefix` adjudicates, and a conductor quoting a
> child's banner via `session output` renders behind `⎿`, which
> `claudeQuotedLinePrefixes` already excludes. A copy-pasted matcher would lose
> both guards.
>
> **Ordering in `ClassifySubstate`: after the busy check, before
> `model-unavailable`.**
>
> ```
> 1. auth-401            (terminal credential failure — outranks a stale busy cue)
> 2. busy indicator      → running
> 3. api-error           (NEW)
> 4. model-unavailable
> 5. idle-at-empty-prompt
> 6. none
> ```
>
> `auth-401` sits ahead of busy because a credential failure is terminal. A
> transport error is not: the session may already have recovered, in which case a
> live spinner is the truth. The cost of a false `api-error` is injecting a prompt
> into a working session, and that asymmetry sets the ordering. The check is
> scoped to the recent tail (same as `hasModelUnavailableNoop`) so a banner
> scrolled up into history stops matching.

> ## 5. Out of scope
>
> - **New notification or TUI surface**, beyond the one glyph a new substate needs
>   in order to render (`cmd/agent-deck/cli_utils.go`,
>   `internal/ui/connection_status.go`).

> ## 6. Verification
>
> **Detector (unit).** The captured 2026-08-07 pane classifies `api-error`. A
> conductor quoting the banner behind `⎿` does not match. Assistant-line prose
> mentioning the banner does not match. A recovered pane carrying both the banner
> and a live spinner classifies `running`, not `api-error`. A banner scrolled
> beyond the recent tail stops matching. `API Error: 401` still classifies
> `auth-401`, not `api-error`.

---

## Spec gap this task resolves

The design says the api-error matcher must inherit `scanClaudeBannerLines`'s
guards. Taken literally that gives a detector that **never fires**. The guard at
`internal/tmux/detector.go:441` is:

```go
if strings.HasPrefix(line, claudeAssistantLinePrefix) && !containsAny(line, claudeBannerStructuralMarkers) {
    continue
}
```

with `claudeBannerStructuralMarkers = []string{" · ", `{"type":"error"`}`. The
captured banner is `⏺ API Error: Unable to connect to API (ENOTFOUND)` — an
assistant-glyph line carrying neither marker, so it is skipped.

Resolution (do exactly this): make the structural set a **parameter** of
`scanClaudeBannerLines`. The auth scans keep passing
`claudeBannerStructuralMarkers`; the api-error scan passes a set that adds the
parenthesised transport codes. Both design §6 requirements then hold — the real
banner matches, and assistant-line prose without a parenthesised code does not.

## Second regression this task must not ship (settled decision — implement as written)

Adding `api-error` ahead of the idle verdict makes **`SubstateStalled`
unreachable for the panes it was built for**, and that is a regression, not a
tidy-up.

`SubstateStalled` is defined by exactly the banner this task now claims
(`internal/tmux/substate.go`: *"a transport failure (\"API Error: Unable to
connect to API (ConnectionRefused)\")"*, and the 2026-07-24 incident recorded in
`internal/session/stall_test.go`). `Instance.Substate()` refines the tmux verdict
through `promoteStalled` (`internal/session/stall.go`), and `promoteStalled`
today refines **only** `SubstateIdleAtEmptyPrompt`. Once `ClassifySubstate`
returns `api-error` first, such a pane never reaches the idle verdict, so it can
never be promoted.

That matters beyond labelling: `session nudge` refuses to send when the substate
is `stalled` (`cmd/agent-deck/session_nudge_cmd.go:212`), and **that refusal is
what stops a send from destroying an operator's in-flight composer draft.**

Required behaviour (this is the approved resolution — do not redesign it):

| pane | substate | consequence |
|---|---|---|
| banner + **empty** composer | stays `api-error` | self-heal resumes it after the 60 s dwell (tasks 02/03/06); there is no operator text to destroy |
| banner + **drafted** composer | `api-error`, then `stalled` once the existing 10-minute `StallDwell` elapses | 🧊 renders, `session nudge` still refuses |

Implement it by letting `promoteStalled` refine `SubstateAPIError` as well as
`SubstateIdleAtEmptyPrompt` (edit 7 below). This does **not** wire
`SubstateStalled` into self-heal: it stays out of `stuckDwellThresholds` and
`actionForSubstate` (task 02 owns both and leaves it out), so design section 5
still holds. Nothing here changes `StallDwell`, the tracker, or the idle branch.

## Acceptance criteria

1. `tmux.SubstateAPIError` exists with value `"api-error"`.
2. `ClassifySubstate` returns it after the busy check, before `model-unavailable`.
3. `scanClaudeBannerLines` takes the structural marker set as a third parameter;
   both pre-existing call sites pass `claudeBannerStructuralMarkers` and their
   behaviour is unchanged.
4. `session.SubstateAPIError` re-export exists.
5. `SubstateLabel` returns a non-empty label for it; the TUI renders a distinct
   glyph for it under **`StatusIdle` / `StatusWaiting`** — *not* under
   `StatusError`, which a transport banner never produces (see edit 6) — and a
   test pins it.
6. All six §6 detector cases pass as unit tests.
7. `promoteStalled` refines `SubstateAPIError` as well as
   `SubstateIdleAtEmptyPrompt`: banner + empty composer stays `api-error`;
   banner + an unchanging draft becomes `SubstateStalled` after `StallDwell`.
   Both branches are pinned by tests.
8. `SubstateStalled` is **not** added to any self-heal map by this task.
9. `gofmt -l` clean on every file touched.

## Edits

### 1. `internal/tmux/detector.go`

Replace the body of `hasClaudeErrorBanner` (currently at line 414-416):

```go
// hasClaudeErrorBanner scans the last 15 non-empty lines (same window as
// hasClaudePrompt) for a banner-shaped error line.
func hasClaudeErrorBanner(content string) bool {
	return scanClaudeBannerLines(content, claudeErrorBannerSubstrings, claudeBannerStructuralMarkers)
}
```

Change the signature and the guard inside `scanClaudeBannerLines`. Replace the
whole function (currently lines 418-451) with:

```go
// scanClaudeBannerLines reports whether any of the last 15 non-empty lines is a
// banner-shaped line containing one of patterns. It carries the over-match
// guards that make banner detection trustworthy — quoted/input lines are
// skipped, and an assistant-turn line must also show one of structural so prose
// merely mentioning the text does not match.
//
// Shared by hasClaudeErrorBanner (any tool-rendered failure banner), the
// auth-specific scan (authFailureBannerPatterns) and the transport scan
// (apiErrorBannerSubstrings) so the three can never drift apart on the guards.
//
// structural is a PARAMETER rather than the package-level
// claudeBannerStructuralMarkers because the transport banner needs a wider set:
// the field capture `⏺ API Error: Unable to connect to API (ENOTFOUND)` carries
// neither the " · " segment separator nor the error JSON, so the shared set
// would skip the one line the transport detector exists to catch.
func scanClaudeBannerLines(content string, patterns, structural []string) bool {
	lines := strings.Split(content, "\n")
	checked := 0
	for i := len(lines) - 1; i >= 0 && checked < 15; i-- {
		line := strings.TrimSpace(StripANSI(lines[i]))
		if line == "" {
			continue
		}
		checked++
		if hasAnyPrefix(line, claudeQuotedLinePrefixes) {
			continue
		}
		// On an assistant-turn line, require a structural banner marker so
		// prose mentioning the banner text is not misread as a live banner.
		if strings.HasPrefix(line, claudeAssistantLinePrefix) && !containsAny(line, structural) {
			continue
		}
		for _, pat := range patterns {
			if strings.Contains(line, pat) {
				return true
			}
		}
	}
	return false
}
```

Then append, immediately after `scanClaudeBannerLines` (i.e. before the
`containsAny` helper):

```go
// apiErrorBannerSubstrings are fragments of the TRANSPORT-failure banner Claude
// Code renders when it cannot reach the API. Field evidence (2026-08-07, a DNS
// outage that wedged 3 of 32 live sessions for 16, 18 and 39 minutes):
//
//	⏺ API Error: Unable to connect to API (ENOTFOUND)
//	✻ Sautéed for 39m 27s
//
// Deliberately a SEPARATE set from claudeErrorBannerSubstrings: a 401 is
// terminal and a transport error is not, so the two earn different substates and
// different recovery. Anchored on the rendered wording and the Node/undici error
// codes, never on a bare token like "API Error".
var apiErrorBannerSubstrings = []string{
	"Unable to connect to API",
	"ENOTFOUND",
	"ECONNREFUSED",
	"ConnectionRefused",
}

// apiErrorBannerStructuralMarkers are the co-signals required on an
// assistant-glyph ("⏺") line before a transport banner is believed.
//
// claudeBannerStructuralMarkers alone would reject the REAL banner: the field
// capture carries neither the " · " segment separator nor the error JSON, so the
// assistant-line guard would skip it and the substate could never fire. The
// PARENTHESISED transport code is the structural shape prose does not carry —
// a conductor writing "the worker showed API Error: Unable to connect to API"
// does not reproduce it — so it joins the set for this scan only.
var apiErrorBannerStructuralMarkers = []string{
	" · ",
	`{"type":"error"`,
	"(ENOTFOUND)",
	"(ECONNREFUSED)",
	"(ConnectionRefused)",
}

// hasClaudeAPIErrorBanner scans the recent pane tail for a TRANSPORT-failure
// banner, reusing the same quoted-line and assistant-prose guards as the auth
// scan so a conductor quoting a child's banner behind "⎿" never matches.
func hasClaudeAPIErrorBanner(content string) bool {
	return scanClaudeBannerLines(content, apiErrorBannerSubstrings, apiErrorBannerStructuralMarkers)
}
```

### 2. `internal/tmux/authfailure.go`

At line 55, change:

```go
	return scanClaudeBannerLines(content, authFailureBannerPatterns)
```

to:

```go
	return scanClaudeBannerLines(content, authFailureBannerPatterns, claudeBannerStructuralMarkers)
```

### 3. `internal/tmux/substate.go`

Add the constant to the `const (...)` block, immediately after
`SubstateAuth401` (line 39):

```go
	// SubstateAPIError marks a TRANSPORT failure banner: Claude could not reach
	// the API at all ("API Error: Unable to connect to API (ENOTFOUND)").
	//
	// Unlike SubstateAuth401 this is RECOVERABLE. Field evidence 2026-08-07: a
	// DNS failure wedged 3 of 32 live sessions for 16, 18 and 39 minutes; the
	// network had recovered long before anyone noticed, and one continuation
	// prompt resumed all three on the first attempt. The panes were never
	// frozen — they repainted and accepted keystrokes — so every content-only
	// heuristic read them as a healthy empty prompt.
	//
	// Pairs with status IDLE or WAITING, never "error": nothing maps a transport
	// banner to StatusError (claudeErrorBannerSubstrings holds only the 401 /
	// login / socket-closed shapes), and that is deliberate — a 401 is terminal
	// and this is not. Anything keyed off this substate must therefore gate on
	// idle/waiting, exactly like SubstateStalled does.
	//
	// A pane in this state whose composer holds an unchanging operator draft is
	// promoted to SubstateStalled after StallDwell (see promoteStalled in
	// internal/session/stall.go): the banner alone is recoverable by one
	// continuation prompt, but text a human typed is not ours to submit.
	SubstateAPIError Substate = "api-error"
```

In `ClassifySubstate`, insert a new step between the busy check (ending line 121)
and the model-unavailable check (starting line 123):

```go
	// 3. TRANSPORT failure banner. Ordered AFTER busy, unlike auth-401: a
	//    credential failure is terminal and stops the spinner, but a transport
	//    error is not — the session may already have recovered, in which case a
	//    live spinner is the truth. The cost of a false api-error is injecting a
	//    prompt into a working session, and that asymmetry sets the ordering.
	//    Scoped to the recent tail, so a banner scrolled up into history stops
	//    matching.
	if hasClaudeAPIErrorBanner(content) {
		return SubstateAPIError
	}
```

Update the doc comment's numbered precedence list above `ClassifySubstate` to
match — replace the `//  3. model-unavailable …` through `//  5. none` lines
(lines 101-103) with:

```go
//  3. api-error — a TRANSPORT failure banner ("Unable to connect to API"). AFTER
//     busy on purpose: a transport error is recoverable, so a live spinner means
//     the session already came back and must not be prompted.
//  4. model-unavailable — the Fable-down no-op loop with no live busy cue.
//  5. idle-at-empty-prompt — sitting at the prompt with nothing happening.
//  6. none      — no distinct refinement.
```

### 4. `internal/session/instance.go`

In the substate re-export `const (...)` block (lines 69-77), add after
`SubstateAuth401`:

```go
	SubstateAPIError          = tmux.SubstateAPIError
```

### 5. `cmd/agent-deck/cli_utils.go`

In `SubstateLabel`, add a case after `case session.SubstateAuth401:`:

```go
	case session.SubstateAPIError:
		return "api unreachable (transport)"
```

### 6. `internal/ui/connection_status.go`

**Do NOT add the glyph to the `status == session.StatusError` block.** A
transport banner never produces `StatusError`: the tmux "error" verdict comes
from `HasErrorBanner` → `hasClaudeErrorBanner` → `claudeErrorBannerSubstrings`
(`internal/tmux/detector.go`), which holds only `API Error: 401`,
`API Error (401`, `Please run /login` and `socket connection closed` — and this
task deliberately keeps the transport markers in a *separate* set and changes no
status derivation. An `api-error` pane stays `idle`/`waiting`, so a glyph gated
on `StatusError` is dead code.

The in-repo precedent is `SubstateStalled`, gated on `StatusIdle || StatusWaiting`
for exactly this reason. Mirror it: replace the stalled block (currently lines
63-72) with a shared idle/waiting block covering both substates:

```go
	// Two substates pair with idle/waiting, NOT error, and both look perfectly
	// healthy in a single frame — quiet pane, visible prompt — which is exactly
	// why each needs its own glyph. Rendered as a plain "○"/"◐" they are
	// indistinguishable from a session that is simply done, and an operator
	// scanning the list has no reason to look closer.
	//
	// "🧊" = stalled: the composer holds text it cannot submit.
	// "🌐" = the API is unreachable (transport). RECOVERABLE, and it reads
	// nothing like a credential failure — an operator must not go hunting for a
	// login when the network is what broke. Deliberately NOT under StatusError:
	// no status derivation maps a transport banner to error, so a glyph gated
	// there would never render.
	if status == session.StatusIdle || status == session.StatusWaiting {
		switch substate {
		case session.SubstateStalled:
			icon = "🧊"
		case session.SubstateAPIError:
			icon = "🌐"
		}
	}
```

Leave the `status == session.StatusError` block (model-unavailable / auth-401)
and the stopped-auth block exactly as they are.

### 7. `internal/session/stall.go` — keep `stalled` reachable

Replace the doc comment and the base guard of `promoteStalled` (currently lines
121-135). **Only the doc comment and the one `if` change**; the tracker, the
capture, `send.ComposerDraft` and every other line stay byte-identical:

```go
// promoteStalled refines an already-computed substate with dwell information,
// returning SubstateStalled when a session that looks idle — or that is showing
// a transport-error banner — is in fact holding an unchanging composer draft.
//
// Two bases are eligible:
//
//   - SubstateIdleAtEmptyPrompt — the original case: content-only heuristics
//     cannot tell a wedged composer from a healthy empty prompt.
//   - SubstateAPIError — the SAME case, now classified one step earlier. This
//     substate is defined by precisely the banner this detector was built from
//     ("API Error: Unable to connect to API (ConnectionRefused)", 2026-07-24),
//     and it is checked ahead of the idle verdict, so refining only the idle
//     base would make SubstateStalled unreachable for the panes it exists to
//     describe.
//
// The split is load-bearing for recovery, not cosmetic:
//
//   - banner + EMPTY composer  -> stays api-error, and self-heal may deliver one
//     continuation prompt after its dwell; there is no operator text to destroy.
//   - banner + DRAFTED composer -> stalled, and `session nudge` REFUSES to send.
//     That refusal is the thing standing between an operator's in-flight draft
//     and a send that consumes it (the --force path is known to consume rather
//     than restore it). Submitting someone else's text is not a decision a
//     status probe gets to make.
//
// SubstateStalled remains reporting-only: it is deliberately absent from
// selfheal's stuckDwellThresholds and actionForSubstate, so promotion here can
// only ever protect a session, never schedule an action against it.
//
// A running, auth-failed or model-unavailable session is already being described
// accurately, and a session with no composer at all has nothing to stall on.
func promoteStalled(base tmux.Substate, src stallSource, tracker *stallTracker) tmux.Substate {
	if tracker == nil {
		return base
	}
	if base != tmux.SubstateIdleAtEmptyPrompt && base != tmux.SubstateAPIError {
		tracker.reset()
		return base
	}
```

Everything from `if src == nil {` onward is unchanged.

Note for the implementer: `stall.go` already imports `internal/tmux`, so no
import change is needed, and `Instance.Substate()` needs no edit — it already
routes through `promoteStalled`.

## Tests — new file `internal/tmux/apierror_test.go`

```go
package tmux

import "testing"

// The captured 2026-08-07 pane. Both lines are real: the banner renders on an
// assistant-glyph line and the whimsical completion line follows it.
const apiErrorPaneCapture = `⏺ Read(internal/session/instance.go)
  ⎿  Read 40 lines

⏺ API Error: Unable to connect to API (ENOTFOUND)
✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`

func claudeDetector() *PromptDetector { return NewPromptDetector("claude") }

// §6: the captured 2026-08-07 pane classifies api-error.
func TestClassifySubstate_TransportBanner_IsAPIError(t *testing.T) {
	if got := claudeDetector().ClassifySubstate(apiErrorPaneCapture); got != SubstateAPIError {
		t.Fatalf("captured transport pane: want %q, got %q", SubstateAPIError, got)
	}
}

// §6: a conductor quoting the banner behind "⎿" does not match.
func TestClassifySubstate_QuotedTransportBanner_NotAPIError(t *testing.T) {
	quoted := `⏺ Bash(agent-deck session output worker-3)
  ⎿  ⏺ API Error: Unable to connect to API (ENOTFOUND)
  ⎿  ✻ Sautéed for 39m 27s

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(quoted); got == SubstateAPIError {
		t.Fatalf("a quoted child banner must not classify api-error, got %q", got)
	}
}

// §6: assistant-line prose mentioning the banner does not match. No
// parenthesised transport code, no " · ", no error JSON — nothing structural.
func TestClassifySubstate_ProseAboutTransportBanner_NotAPIError(t *testing.T) {
	prose := `⏺ The worker showed API Error: Unable to connect to API and never recovered, so I restarted it.

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(prose); got == SubstateAPIError {
		t.Fatalf("assistant prose must not classify api-error, got %q", got)
	}
}

// §6: a recovered pane carrying BOTH the banner and a live spinner classifies
// running. A transport error is recoverable, so the spinner is the truth.
func TestClassifySubstate_TransportBannerWithLiveSpinner_IsRunning(t *testing.T) {
	recovered := `⏺ API Error: Unable to connect to API (ENOTFOUND)
✻ Sautéed for 39m 27s
⠋ Reticulating… (12s · ↓ 431 tokens · esc to interrupt)`
	if got := claudeDetector().ClassifySubstate(recovered); got != SubstateRunning {
		t.Fatalf("recovered pane with a live spinner: want %q, got %q", SubstateRunning, got)
	}
}

// §6: a banner scrolled beyond the recent tail stops matching. The scan window
// is the last 15 NON-EMPTY lines, so 20 lines of later output bury it.
func TestClassifySubstate_TransportBannerScrolledAway_NotAPIError(t *testing.T) {
	buried := "⏺ API Error: Unable to connect to API (ENOTFOUND)\n"
	for i := 0; i < 20; i++ {
		buried += "⏺ Ordinary assistant output line\n"
	}
	buried += "╭──────────────────────────────────────────╮\n│ ❯                                        │\n╰──────────────────────────────────────────╯"
	if got := claudeDetector().ClassifySubstate(buried); got == SubstateAPIError {
		t.Fatalf("a banner scrolled out of the tail must not classify api-error, got %q", got)
	}
}

// §6: "API Error: 401" still classifies auth-401, not api-error. auth-401 is
// checked FIRST and the two marker sets are disjoint.
func TestClassifySubstate_Auth401_StillAuth401(t *testing.T) {
	auth := `⏺ API Error: 401 · Please run /login

╭──────────────────────────────────────────╮
│ ❯                                        │
╰──────────────────────────────────────────╯`
	if got := claudeDetector().ClassifySubstate(auth); got != SubstateAuth401 {
		t.Fatalf("401 banner: want %q, got %q", SubstateAuth401, got)
	}
}

// The transport markers must not leak into the auth-hold decision: a transport
// failure is restart-recoverable and must stay outside the credential hold.
func TestIsAuthFailureContent_TransportBanner_NotAuthFailure(t *testing.T) {
	if IsAuthFailureContent("claude", apiErrorPaneCapture) {
		t.Fatal("a transport banner must never arm the credential auth hold")
	}
}

// A non-claude tool has no substate heuristics at all.
func TestClassifySubstate_NonClaude_NoAPIError(t *testing.T) {
	if got := NewPromptDetector("codex").ClassifySubstate(apiErrorPaneCapture); got != SubstateNone {
		t.Fatalf("non-claude tool: want %q, got %q", SubstateNone, got)
	}
}
```

## Tests — append to `internal/session/stall_test.go`

These pin acceptance criterion 7 — both branches of the api-error/stalled split.
They reuse the file's existing helpers (`withStallClock`, `fakePane`,
`paneWithComposer`); add no new imports.

```go
// A transport banner over an EMPTY composer stays api-error. This is the branch
// self-heal acts on: there is no operator text to destroy, so one continuation
// prompt is safe.
func TestPromoteStalled_APIErrorEmptyComposer_StaysAPIError(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	pane := &fakePane{content: paneWithComposer("")}
	tracker := &stallTracker{}

	for i := 0; i < 3; i++ {
		now = now.Add(time.Hour)
		if got := promoteStalled(tmux.SubstateAPIError, pane, tracker); got != tmux.SubstateAPIError {
			t.Fatalf("an empty composer under a transport banner must stay %q, got %q", tmux.SubstateAPIError, got)
		}
	}
}

// A transport banner over a FROZEN operator draft is promoted to stalled once
// StallDwell elapses. Without this, classifying api-error ahead of the idle
// verdict would make SubstateStalled unreachable for the exact pane it was built
// from (2026-07-24) — and `session nudge` would stop refusing, which is the only
// thing protecting the operator's draft from being consumed by a send.
func TestPromoteStalled_APIErrorFrozenDraft_BecomesStalled(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	withStallClock(t, &now)

	pane := &fakePane{content: paneWithComposer("target release-6.18.1")}
	tracker := &stallTracker{}

	// First observation starts the clock — never stalled on sight.
	if got := promoteStalled(tmux.SubstateAPIError, pane, tracker); got != tmux.SubstateAPIError {
		t.Fatalf("first observation: want %q, got %q", tmux.SubstateAPIError, got)
	}

	// Still inside the dwell: self-heal's own 60s dwell has passed by now, so
	// this window is precisely where the two detectors disagree — and api-error
	// is the correct answer only until the draft proves itself frozen.
	now = now.Add(9 * time.Minute)
	if got := promoteStalled(tmux.SubstateAPIError, pane, tracker); got != tmux.SubstateAPIError {
		t.Fatalf("inside dwell: want %q, got %q", tmux.SubstateAPIError, got)
	}

	// Past the dwell with the same unchanged draft: wedged, and the nudge gate
	// must see it.
	now = now.Add(2 * time.Minute)
	if got := promoteStalled(tmux.SubstateAPIError, pane, tracker); got != tmux.SubstateStalled {
		t.Fatalf("past dwell: want %q, got %q", tmux.SubstateStalled, got)
	}
}
```

`TestPromoteStalled_OnlyRefinesIdleVerdict` already pins the pass-through bases
(`running`, `auth-401`, `model-unavailable`, `none`) and must keep passing
unchanged — do **not** add `SubstateAPIError` to its list.

## Tests — append to `internal/ui/connection_status_test.go`

This is the test that would have caught the dead glyph. Add these rows to
`TestRowStatusGlyph`'s table (criterion 5):

```go
		{"idle + api-error substate", session.StatusIdle, session.SubstateAPIError, false, "🌐"},
		{"waiting + api-error substate", session.StatusWaiting, session.SubstateAPIError, false, "🌐"},
		{"api-error glyph does NOT render under error status", session.StatusError, session.SubstateAPIError, false, "✕"},
		{"api-error glyph does NOT render under running", session.StatusRunning, session.SubstateAPIError, false, "●"},
		{"archived overrides the api-error glyph", session.StatusIdle, session.SubstateAPIError, true, "■"},
		{"stalled still renders alongside api-error", session.StatusIdle, session.SubstateStalled, false, "🧊"},
```

The third row is the load-bearing one: it fails against the naive
"`case session.SubstateAPIError` inside the `StatusError` block" edit, and passes
only once the glyph is gated on idle/waiting.

## Verification

Run from the worktree root. Do **not** pipe — read the bare exit status.

```sh
cd /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume
gofmt -l internal/tmux/detector.go internal/tmux/authfailure.go internal/tmux/substate.go internal/tmux/apierror_test.go internal/session/instance.go internal/session/stall.go internal/session/stall_test.go cmd/agent-deck/cli_utils.go internal/ui/connection_status.go internal/ui/connection_status_test.go
```
Expected output: **nothing** (empty). Any filename printed means run `gofmt -w` on it.

```sh
go build ./...
```
Expected: no output, exit 0.

```sh
go test ./internal/tmux/ -run 'APIError|ClassifySubstate|AuthFailure|ErrorBanner' -v
```
Expected: every listed test prints `--- PASS`, final line `ok  	github.com/asheshgoplani/agent-deck/internal/tmux`. The eight new
`Test...` functions above must all appear as PASS; `TestClassifySubstate_TransportBanner_IsAPIError`
is the run-specific sentinel — if it is absent from the output the test file was
not compiled into the package.

```sh
go test ./internal/session/ -run PromoteStalled -v; echo "EXIT=$?"
```
Expected: `EXIT=0`, with **every** pre-existing `TestPromoteStalled_*` still
PASS plus the two new ones. Run-specific sentinel:
`TestPromoteStalled_APIErrorFrozenDraft_BecomesStalled` must appear as
`--- PASS` — its absence means the promotion guard was not widened and `stalled`
is now unreachable for transport-wedged panes.

```sh
go test ./internal/ui/ -run RowStatusGlyph -v; echo "EXIT=$?"
```
Expected: `EXIT=0`. Run-specific sentinel: the subtest
`TestRowStatusGlyph/api-error_glyph_does_NOT_render_under_error_status` must
appear as `--- PASS`. If it fails with `got "🌐", want "✕"`, the glyph was gated
on `StatusError` instead of idle/waiting.

```sh
go test ./internal/ui/ -run ConnectionStatus -v; echo "EXIT=$?"
```
Expected: `EXIT=0`. (If `internal/ui` reports a zoxide-related failure unrelated
to `ConnectionStatus`, that is a known sandbox flake — confirm the failing test
name is not one you touched, and record it in the Record section.)

Structural check that this task did not wire `stalled` into self-heal (criterion 8):
```sh
grep -rn 'SubstateStalled' internal/selfheal/
```
Expected: **no output**. `SubstateStalled` must not appear anywhere in
`internal/selfheal/` after this task.

```sh
go vet ./internal/tmux/ ./internal/session/ ./internal/ui/ ./cmd/agent-deck/
```
Expected: no output, exit 0.

## Commit

```sh
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume add \
  internal/tmux/detector.go internal/tmux/authfailure.go internal/tmux/substate.go \
  internal/tmux/apierror_test.go internal/session/instance.go \
  internal/session/stall.go internal/session/stall_test.go \
  cmd/agent-deck/cli_utils.go internal/ui/connection_status.go \
  internal/ui/connection_status_test.go
git -C /Users/doozyx/DoozyX/agent-deck/.worktrees/feature-selfheal-auto-resume commit -m "feat(tmux): classify transport failures as the api-error substate

A DNS outage on 2026-08-07 wedged 3 of 32 live sessions for 16-39 minutes.
Each pane ended on \"API Error: Unable to connect to API (ENOTFOUND)\", which
matched none of the existing banner markers, so all three classified as
idle-at-empty-prompt and nothing noticed.

Adds SubstateAPIError, scanned through the shared banner guards so a conductor
quoting a child's banner behind the tool-result connector still does not match.
scanClaudeBannerLines now takes its structural marker set as a parameter: the
real banner renders on an assistant-glyph line carrying neither the segment
separator nor the error JSON, so the shared set would have skipped the one line
this detector exists to catch.

Ordered after the busy check, unlike auth-401: a transport error is recoverable,
so a live spinner means the session already came back.

promoteStalled is widened to refine api-error too. SubstateStalled is defined by
exactly this banner, and classifying api-error ahead of the idle verdict would
otherwise make it unreachable for the panes it was built from — silently
disarming the nudge gate that keeps a send from consuming an operator's draft.
A banner over an empty composer stays api-error and is recoverable; a banner over
a frozen draft still becomes stalled after the 10-minute dwell.

The TUI glyph is gated on idle/waiting, not error: no status derivation maps a
transport banner to StatusError, so a glyph gated there would never render."
```

## Interfaces

### consumes
- `internal/tmux/detector.go`: `scanClaudeBannerLines(content string, patterns []string) bool` (existing, being widened), `claudeQuotedLinePrefixes []string`, `claudeAssistantLinePrefix string`, `claudeBannerStructuralMarkers []string`, `containsAny(s string, subs []string) bool`, `hasAnyPrefix(s string, prefixes []string) bool`, `StripANSI(string) string`, `NewPromptDetector(tool string) *PromptDetector`
- `internal/tmux/substate.go`: `Substate string`, `SubstateNone`, `SubstateRunning`, `SubstateAuth401`, `SubstateModelUnavailable`, `(*PromptDetector).ClassifySubstate(content string) Substate`, `(*PromptDetector).hasClaudeBusyIndicator(content string) bool`, `hasModelUnavailableNoop(content string) bool`
- `internal/tmux/authfailure.go`: `authFailureBannerPatterns []string`, `IsAuthFailureContent(tool, content string) bool`
- `cmd/agent-deck/cli_utils.go`: `SubstateLabel(sub session.Substate) string`
- `internal/ui/connection_status.go`: `rowStatusGlyph(status session.Status, substate session.Substate, archived bool) (string, lipgloss.Style)` — specifically its `StatusIdle || StatusWaiting` substate block
- `internal/session/stall.go`: `promoteStalled(base tmux.Substate, src stallSource, tracker *stallTracker) tmux.Substate`, `stallSource`, `stallTracker`, `StallDwell`
- `internal/session/stall_test.go`: `withStallClock(t, *time.Time)`, `fakePane`, `paneWithComposer(text string) string`
- `internal/tmux/substate.go`: `SubstateStalled`, `SubstateIdleAtEmptyPrompt`

### produces
- `internal/tmux/substate.go`: `const SubstateAPIError Substate = "api-error"`
- `internal/tmux/detector.go`: `func hasClaudeAPIErrorBanner(content string) bool`; `var apiErrorBannerSubstrings []string`; `var apiErrorBannerStructuralMarkers []string`
- `internal/tmux/detector.go`: **changed signature** `func scanClaudeBannerLines(content string, patterns, structural []string) bool`
- `internal/session/instance.go`: `const SubstateAPIError = tmux.SubstateAPIError` (re-export, type `session.Substate`)
- `cmd/agent-deck/cli_utils.go`: `SubstateLabel(session.SubstateAPIError) == "api unreachable (transport)"`
- `internal/session/stall.go`: **widened base guard** — `promoteStalled` now refines `SubstateAPIError` in addition to `SubstateIdleAtEmptyPrompt`. Signature unchanged. Consequence downstream: `(*Instance).Substate()` reports `stalled` for a transport-banner pane holding a frozen draft, so `session nudge`'s existing `SubstateStalled` refusal (`cmd/agent-deck/session_nudge_cmd.go`) keeps covering it. **Task 06's `buildSelfHealCandidate` reads `CachedSubstate()`, which does not route through `promoteStalled`, so `SubstateStalled` never reaches self-heal — that separation is intentional and must be preserved.**
- `internal/ui/connection_status.go`: `rowStatusGlyph` renders `"🌐"` for `SubstateAPIError` under `StatusIdle`/`StatusWaiting` only

## Record (append-only)
