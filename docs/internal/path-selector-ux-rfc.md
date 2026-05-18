# RFC: Unified path-selector input model in the New-Session dialog

Status: **Draft (for discussion, not merge)**
Owner: TBD (initiated by UX-rethink worker, 2026-05-18)
Target: v1.10 line
Refs: #1020, #983, #896, #885, #1021, Feedback Hub #600 (@balazser, @Showtimes)

---

## 0. TL;DR

Three independent users complained about the same dialog within two weeks.
PR #1021 fixed @JMBattista's exact symptom but the underlying state machine
is still discoverability-hostile: the path field has **three implicit
modes** (soft-select / popup-active / edit), discriminated by a triple of
booleans, with **four different keys** (Space, Enter, Right, type-anything)
that can each transition you between them and **at least one transition
that is not round-trippable** (Esc out of popup-active drops you in *edit*
mode, not back to soft-select). The hint footer changes per-mode but the
focus indicator does not.

This RFC names the state machine, presents two redesigns, and recommends
**Model A (Modal: explicit popup state, visually obvious)** with a small
prototype that an author can pick up as a draft PR.

---

## 1. Current behavior — truth table

### 1.1 State variables

The path field's behaviour is governed by these in-memory flags
(`internal/ui/newdialog.go`):

| Variable | Meaning |
|---|---|
| `cur == focusPath` | dialog focus is on the path row |
| `pathInput.Focused()` | textinput cursor is active in the path string |
| `pathSoftSelected` | path text rendered reverse-video, ready-to-replace-on-type |
| `suggestionsActive` | popup is in "navigation" mode (arrows move popup, not form) |
| `suggestionsHidden` | popup has been explicitly dismissed for this focus visit |
| `len(pathSuggestions) > 0` | external feeder (home.go:6477) returned ≥1 recent path |

### 1.2 Reachable states on the path field

| # | `pathSoftSelected` | `pathInput.Focused` | `suggestionsActive` | Name we will use | How you get here |
|---|---|---|---|---|---|
| S1 | `true` | `false` | `false` | **Soft-select** | Tab-land on path with a pre-filled value (the cwd default, or after `updateFocus()` sees `pathInput.Value() != ""`) |
| S2 | `false` | `true`  | `false` | **Edit** | Type any rune from S1; Esc/Left from S3; Ctrl+W from S1/S2; Backspace from S1 |
| S3 | `false` | `false` | `true`  | **Popup-active** | Space/Enter/Right from S1; Enter from S2; auto-activate on Up/Down from S2 when `len(pathSuggestions)>0 && !suggestionsHidden` (#983) |
| S4 | `false` | `true`  | `false` (popup hidden) | **Edit (popup dismissed)** | Tab autocomplete-applied; suggestion picked via Enter; `suggestionsHidden=true` set somewhere |

S1 ≡ "I just landed here". S2 ≡ "I'm typing". S3 ≡ "I'm navigating the
dropdown". S4 ≡ "popup-suppressed edit" — practically indistinguishable
from S2 to the user.

### 1.3 Truth table (rows = state, cols = key)

For each (state, key) cell, the value is `<action> → <next state>`. `cur+1`
means focus moves to the next form field via `moveFocus(1)`. `cur-1`
likewise. ⟂ means "no-op / passes to textinput". ☓ means "dialog closes".

| Key | S1 Soft-select | S2 Edit | S3 Popup-active | S4 Edit (popup hidden) |
|---|---|---|---|---|
| `Up` | escape → cur-1 *(post-#1021)* | auto-activate popup → S3 *if `len(sugg)>0`*; otherwise cur-1 | popup cursor − 1 → S3 | cur-1 |
| `Down` | escape → cur+1 *(post-#1021)* | auto-activate popup → S3 *if `len(sugg)>0`*; otherwise cur+1 | popup cursor + 1 → S3 | cur+1 |
| `Left` | exit soft-select, focus textinput → S2 | move cursor in text | exit popup → S2 (textinput.Focus) | move cursor in text |
| `Right` | enter popup-active → S3 | move cursor in text | (consumed, no-op) | move cursor in text |
| `Space` | enter popup-active → S3 | passes to textinput (space inserted into path) | apply highlighted suggestion + close popup, stay in form → S2 | passes to textinput |
| `Enter` | enter popup-active → S3 | enter popup-active → S3 | apply highlighted + `moveFocus(+1)` → next field's idle state | submit form (if Validate passes) |
| `Tab` | autocomplete if path is partial **else** cur+1 *(also blocks advance if value is non-empty non-dir, #896 problem 1)* | as S1; cycles `pathCycler` if previously activated | dismiss popup + cur+1 | as S1 |
| `Shift+Tab` | dismiss popup + cur-1 | dismiss popup + cur-1 | dismiss popup + cur-1 | dismiss popup + cur-1 |
| `Esc` | ☓ close dialog | ☓ close dialog | exit popup → S2 (focus textinput) | ☓ close dialog |
| `Ctrl+N` | popup cursor +1, `pathInput.Focus()` → S4-like (popup not active, cursor advanced) | same | popup cursor +1 → S3 | same |
| `Ctrl+P` | popup cursor -1, `pathInput.Focus()` → S4-like | same | popup cursor -1 → S3 | same |
| `Ctrl+W` | path-aware backward word delete + `pathInput.Focus()` → S2 | same | n/a (consumed by dropdown switch fallthrough) | same |
| any printable rune | clears value, focuses textinput, rune inserted → S2 | inserted into path | (consumed, no-op) | inserted |
| `Backspace`/`Delete` | clears value → S2 | textinput delete | (consumed, no-op) | textinput delete |

(Key gaps confirmed by reading `internal/ui/newdialog.go` lines 1299–1942
and exercised live via `tmux send-keys` against `agent-deck -p uxprobe`,
captures stored under `/tmp/exec-ux-rethink-path/screenshots/`.)

### 1.4 Confusion classes (where the same key has different effects)

Each row is a key that means different things in different states. These
are the discoverability bugs the three user reports map to.

| Class | Key | Difference | Why a user trips |
|---|---|---|---|
| **C1** | `Up`/`Down` | S1 → escape form; S2 → maybe activate popup; S3 → popup cursor | Same visible position "▶ Path:" before and after Tab-landing; only `pathSoftSelected` flips and there is no visible indicator of it. @JMBattista hit this when #983 made S2's behaviour leak into S1. #1021 patched S1 only. The S2-vs-S4 distinction is still silent. |
| **C2** | `Enter` | S1 / S2 → open popup; S3 → apply suggestion + advance focus; S4 → submit form | The same key submits or doesn't submit depending on which sub-mode of "I'm on the path field" you're in. This is the @Showtimes "gives up and types the whole path by hand" pattern. |
| **C3** | `Space` | S1 → open popup; S2 → inserts a space into the path; S3 → apply + close popup | Especially fragile: users with a single backspace from S1 to S2 lose the popup-entry gesture without realising it. |
| **C4** | `Left` / `Right` | S1: `Left` → S2, `Right` → S3 (asymmetric); S2/S4: cursor motion in textinput; S3: `Left` → S2 | Two arrows that look like cursor motion sometimes change mode. JMBattista's original suggestion was to make this pair the *only* way to enter/leave the popup — implicitly because nothing else feels safe. |
| **C5** | popup visibility ≠ popup activeness | S1 + suggestions visible — looks identical to S3 except for the small popup hint string size | The popup's *internal* hint line ("→/Space browse" vs "↑/↓ navigate │ Space select │ Enter select & close") is the only visible cue. Easy to miss in a 60-row terminal. |
| **C6** | Esc asymmetry | S3 Esc → exit popup; everywhere else Esc → close dialog | A user who just opened the popup and pressed Esc to "undo" expects to be back on the path field. They are — but in S2, not S1. Now `Up`/`Down` no longer escape unconditionally (they may auto-activate the popup again). Round-trip is not idempotent. |
| **C7** | dropdown contents | Always shows synthetic "Type custom path…" at index 0 even when zero real suggestions exist | When suggestion list is empty (fresh profile, first use), the popup still appears and looks navigable but has nothing to navigate. This is the worst first-impression case. |

---

## 2. User reports — verbatim and mapping to states

### 2.1 @balazser — Feedback Hub #600, 2026-05-04, v1.7.78 linux amd64

> Sometimes navigation feels hard, especially when creating a new session
> and selecting a path. **I'm often confused about when to use Tab versus
> the arrow keys.**

Maps to: **C1, C4, C5**. The Tab-vs-arrow confusion is the entire S1/S2/S3
triangle. Tab does autocomplete cycle *if* the path is partial, otherwise
moves focus; arrows do form-nav *if* you're in S1, popup-nav *if* you're
in S3, and conditionally one-or-the-other in S2.

> The status indicators at the top, such as the green dots, are also not
> clear to me. I'm not sure what purpose they serve.

Out of scope for this RFC — see `drafts/issue-green-dot-legend.md`.

> Since many things use Emacs-style bindings, I miss being able to use
> Ctrl+P and Ctrl+N for up/down navigation in menus.

Already addressed by **PR #885** (@MauriceDHanisch, open), which threads
`Ctrl+N`/`Ctrl+P` through every list view. See §6.

> Also, when I press Ctrl+Z in a session to suspend it, it creates an
> empty message.

Out of scope — see `drafts/issue-ctrlz-empty-message.md`.

### 2.2 @Showtimes — Feedback Hub #600, 2026-05-17 22:29 UTC

> I agree with this. **I also have issues with selecting a path and
> sometimes give up and have to type the whole path by hand.**

Maps to: **C2, C3, C5, C7**. The "give up and type the whole path" pattern
is the failure tail of every state-machine confusion: when no key obviously
"selects" a suggestion, the only safe escape is to retype the whole string
from scratch — bypassing the popup entirely.

### 2.3 @JMBattista — Issue #1020, 2026-05-17 03:50 UTC

> The path selector was recently changed and reacts to the up/down arrow
> much more aggressively. This is causing me a bit of a miserable
> experience any time I need to actually move the cursor above/below that
> section. Could we maybe look at something like **pressing right arrow to
> move into the path nagivation and left arrow to leave it**? Or changing
> the meaning of enter while inside path selection or something else?

Maps to: **C1, C4**. JMBattista's symptom is exactly the S1 → S3
auto-activation that #983 introduced. **#1021 has resolved his exact
keystroke**: Up/Down from S1 now escape. But the neighbouring states (S2
auto-activate, S3 Esc dropping into S2 rather than S1, popup visible in S1
without explicit activation) are still confusing — JMBattista's suggested
fix ("Right enters, Left exits") is in fact already implemented for S1,
just not advertised, and broken for S3 → S2 → S1 round-trip.

---

## 3. Browser-harness evidence

Captured 2026-05-18 by `tmux send-keys` against
`/tmp/exec-ux-rethink-path/agent-deck-bin -p uxprobe` (clean
`HOME=/tmp/exec-ux-rethink-path/adeck-test-home`, no prior sessions,
suggestion list empty). Files at `/tmp/exec-ux-rethink-path/screenshots/`.

| File | State reached | What it proves |
|---|---|---|
| `02-new-dialog-opened.txt` | Name focused | Footer hint: `Tab next/accept │ ↑↓ navigate │ Enter create │ Esc cancel` |
| `04-after-Tab2-path-softselect.txt` | S1 (Path soft-select) | Popup visible with narrow body "→/Space browse"; footer changes to `Type to replace │ Enter browse list │ ← edit │ Tab next │ Esc cancel` |
| `05-down-from-softselect.txt` | S1 → cur+1 (Command focused) | ✓ #1021 fix verified: Down escapes from soft-select |
| `07-up-from-softselect.txt` | S1 → cur-1 (MultiRepo focused) | ✓ #1021 fix verified: Up escapes from soft-select |
| `09-space-popup-active.txt` | S3 (Popup-active) | Same visual focus indicator (▶ Path), popup widens, footer becomes `↑/↓ navigate │ Space/Enter select │ Tab next │ Esc back` |
| `10-popup-active-down.txt` | S3 (single-entry dropdown) | With zero real suggestions, the popup has only the synthetic "Type custom" entry — Down cursors wrap-in-place (C7) |
| `11-popup-active-left.txt` | S3 → S2 (Edit) | Left from S3 returns to passive Path view but pathInput is now focused (not soft-selected); footer reverts |
| `15-down-from-editmode.txt` | S2 → cur+1 (Command) | With empty `pathSuggestions`, auto-activate gate fails so Down escapes from S2 too — fragile invariant, breaks the moment a prior session adds an entry |

Most-damning pair: `04` vs `09` — the *same* "▶ Path:" focus indicator,
the *same* path string visible, but pressing `Down` does opposite things
(escape vs popup-nav). Only the popup body and the footer string change,
both physically small.

---

## 4. Proposed models

Both proposals start from the same simplification: **collapse S1/S2/S4
into a single "Edit" mode**, and make S3 the only modal state with its own
hard-to-miss visual signature. The difference is what becomes the dominant
gesture.

### 4.1 Model A — Modal (recommended)

**Tagline**: *"You're either editing text or driving the popup. The dialog
always tells you which."*

| Change | Detail |
|---|---|
| Remove S1 | Drop `pathSoftSelected`. Tab-landing on a non-empty path always enters S2 (textinput focused, cursor at end, full string visible). No "reverse-video soft-select" rendering. |
| One key to open popup | `Enter` on the path field opens S3 (consistent with @JMBattista's "or changing the meaning of enter while inside path selection"). `Space` becomes a literal space character in the path again. `Right` is always cursor motion. |
| Visual modal cue | When S3 is active, render the entire path row with a thicker / colored border, **and** prefix the label with a `[POPUP]` chip, **and** change the footer hint. Three redundant cues; one is enough but defensive. |
| Esc semantics | S3 Esc → S2 (path field, textinput focused). S2 Esc → close dialog. Consistent with "Esc unwinds one layer". |
| Up/Down | S2: form-field navigation. S3: popup-cursor navigation. **Never** auto-activate popup. This deletes #983's auto-activate and re-tests #896 sub-bugs 3+4 under the new path. |
| `Ctrl+P`/`Ctrl+N` | Aliases for Up/Down everywhere — adopt #885's diff. |
| Tab | Unchanged: cycles autocomplete on a partial path, otherwise advances focus. |
| First-render popup | Suppress until user actually presses `Enter` (or focuses with prior suggestions and explicitly asks). Removes C5 + C7 entirely. |

**Pros**: One state to remember (S3 = popup active). No reachable
state where the user must guess what the popup is doing. Sound under user
report patterns: @balazser ("Tab vs arrows"), @Showtimes ("gives up"),
@JMBattista ("Enter or Right"). Removes ~120 LOC.

**Cons**: Loses the "ready-to-replace-on-type" behaviour from soft-select.
Mitigation: select-all-on-Tab-land (textinput's built-in `setCursorMode`)
so the first printable rune still replaces, but with a normal selection
highlight (familiar) instead of the bespoke reverse-video render.

### 4.2 Model B — Drawer (alternative)

**Tagline**: *"The popup never auto-shows. You ask for it with one
explicit gesture."*

| Change | Detail |
|---|---|
| Remove S1 entirely | Same as Model A. |
| Hide popup by default | The dropdown is never visible unless the user presses `Ctrl+Space` (or a configurable "open suggestions" chord). |
| One key to open *and* one to close | `Ctrl+Space` opens; `Esc` or `Ctrl+Space` again closes. No other key opens it. |
| Up/Down/Tab on the path field | Always form-nav (Up/Down) or autocomplete-then-form-nav (Tab). They never touch the popup. |
| When popup is open | Up/Down/Ctrl+N/Ctrl+P navigate; Enter applies + closes; Esc closes without applying. Popup steals focus visibly (drawer slides in from the right side of the path field). |
| Tab autocomplete | Unchanged. |

**Pros**: Zero accidental popup activation — the only way to get into it
is one specific chord. Eliminates every C-class confusion at once. New
users get a textinput that behaves like a textinput.

**Cons**: Suggestions discoverability drops. Today a user opens the dialog
and immediately sees recent paths; under Model B they need to know
`Ctrl+Space`. Mitigation: footer always shows `^␣ suggestions (N)`
when there are ≥1 suggestion.

### 4.3 Side-by-side

| Question | Modal (A) | Drawer (B) |
|---|---|---|
| How many implicit modes? | 2 (Edit / Popup) | 2 (Edit / Drawer) — but only one default |
| What does Enter do on path? | Open popup | Submit form (no popup intercept) |
| First-time discoverability of suggestions | Footer hint when path focused | Footer hint shows `^␣ suggestions (N)` |
| Risk of "give-up-and-type" (C7) | Low (popup hidden until asked) | Lowest (popup never auto-shows) |
| Reuses existing keystrokes / muscle memory | Reuses Enter (matches today) | Introduces a new chord (Ctrl+Space) |
| Compat with #885 (Ctrl+N/Ctrl+P) | Direct adopt | Direct adopt |
| LOC delta | ~ −120 net | ~ −150 net (popup render moves behind a chord) |
| Migration cost | Touches #983 + #1021 tests | Touches #896 + #983 + #1021 tests |

---

## 5. Recommendation

**Pick Model A (Modal).**

Reasoning:

1. The existing Enter binding (`Enter on path → open popup`) is the most
   muscle-memory-compatible gesture for users who came from v1.5+. Model A
   keeps it; Model B reuses it for "submit" and so silently changes what
   pressing Enter does — exactly the C2 confusion class we are trying to
   remove.
2. Discoverability of suggestions is a known agent-deck value (the
   feature was specifically built in #896 to let users avoid retyping
   paths). Model B's "hide by default" undermines that.
3. Model A is strictly a *subtraction* of state. Model B is a subtraction
   plus a new chord. Less to invent, less to teach, less to test.
4. The cost in lost UX is the "soft-select reverse-video" feel, which two
   of three reporters (@balazser, @Showtimes) explicitly named as
   confusing. Replacing it with the standard textinput selection is *also*
   what those reporters expect.

### 5.1 Migration considerations

- **Drop `pathSoftSelected` and every code path branched on it**
  (`updateFocus`, `Update`, `View`). That gates the elimination of S1.
- **PR #1021's regression test** (`TestNewDialog_PathSelector_UpDownEscapesField_RegressionFor1020`)
  is asserting *S1-specific* behaviour. Under Model A there is no S1, so
  the test needs to be rewritten as
  `TestNewDialog_PathSelector_UpDownAlwaysEscapesUnlessPopupActive` —
  same intent, no `pathSoftSelected` precondition. The semantic guarantee
  to JMBattista is preserved (Up/Down on path → escape unless popup
  active); the implementation discriminator is just `suggestionsActive`.
- **PR #983's two regression tests** (`TestNewDialog_PopupEnter_*` and
  `TestNewDialog_PopupArrows_*`) become *post-condition* tests: under
  Model A, the popup must be explicitly entered (via Enter) before arrows
  navigate it. The auto-activate-on-arrow path goes away. Re-author the
  tests to:
  1. Activate the popup via `Enter`.
  2. Assert arrows navigate, Enter applies + advances focus, Esc returns
     to S2.
- **PR #896 problem 1** (Tab doesn't advance from a non-existent path):
  unchanged. Keep.
- **PR #896 problem 2** (Ctrl+W path-aware delete): unchanged.
- **PR #885** ships Ctrl+N/Ctrl+P aliases everywhere. Direct adopt; the
  popup-nav cases in #885 stay correct under Model A.
- **Help screen (`?`)** needs a small block listing the path-field
  bindings as a single column instead of "well, it depends".

### 5.2 Out-of-scope but adjacent

- **Multi-repo mode** (`focusMultiRepo`, `multiRepoEditing`) has its own
  Enter-as-edit/Enter-as-save toggle. Worth re-reviewing under the same
  modal lens in a follow-up, but not blocked by this RFC.
- **Model suggestions popup** (`focusModel`, `modelSuggestionActive`)
  mirrors the path popup exactly. Apply Model A to it in the same diff
  for consistency, or schedule a parallel cleanup.

---

## 6. PR #885 (emacs Ctrl+N/Ctrl+P nav) — disposition

**Decision: KEEP_OPEN** (review and merge).

Why not takeover:
- The author (@MauriceDHanisch) has shipped a clean, surgical diff:
  additive aliases, no behavioural changes to existing keys.
- Every changed surface has a new test (six in `internal/ui/`).
- The diff orthogonally addresses @balazser's third point. It does not
  block this RFC; merging it *strengthens* the RFC's footing (Ctrl+N/P is
  assumed everywhere by §4).

Suggested review note for the maintainer:
> Adopt this PR independently of the path-selector RFC. Once Model A
> lands, the popup-nav block of #885 needs a one-line follow-up to drop
> the soft-select branch — but that's RFC follow-up, not a review block.

---

## 7. Test strategy

A failing test the new model must pass. (Pseudocode, would live in
`internal/ui/path_selector_v2_test.go`.)

### 7.1 Modal invariants (Model A)

```go
// One mode at a time.
func TestPathField_ExactlyOneStateActive(t *testing.T) {
    for _, scenario := range []scenario{tabLand, typing, popupOpen, popupClosed} {
        d := setup(scenario)
        if popupActive(d) == textInputFocused(d) {
            // Model A: when popup is active, textinput is blurred.
            // When popup is inactive, textinput is focused.
            // Never both, never neither.
        }
    }
}

// Round-trippable popup gesture.
func TestPathField_EnterEscIsIdempotent(t *testing.T) {
    d := openNewDialogOnPath("/tmp/foo")
    pre := snapshot(d)
    d.Update(enter)        // open popup
    d.Update(esc)          // close popup
    if snapshot(d) != pre { t.Fatal("Enter+Esc must be a no-op") }
}

// Arrows never auto-activate popup.
func TestPathField_ArrowsNeverAutoActivatePopup(t *testing.T) {
    d := openNewDialogOnPathWithSuggestions("/tmp/foo", []string{"/a", "/b"})
    d.Update(downArrow)
    if popupActive(d) { t.Fatal("Down auto-activated popup; Model A forbids") }
    if d.currentTarget() != focusCommand { t.Fatal("Down must escape to next field") }
}

// Enter opens popup.
func TestPathField_EnterOpensPopup(t *testing.T) {
    d := openNewDialogOnPath("/tmp/foo")
    d.Update(enter)
    if !popupActive(d) { t.Fatal("Enter on path must open popup") }
}

// Footer hint matches state.
func TestPathField_FooterHintMatchesState(t *testing.T) {
    cases := []struct{
        setup    func() *NewDialog
        wantHint string
    }{
        {tabLandOnPath, "Type to filter │ Enter list │ Tab next │ Esc cancel"},
        {popupActive,   "↑↓ navigate │ Enter select │ Esc close"},
    }
    // assert exact match — protects against the C5 mis-cue today
}
```

### 7.2 Regression tests to migrate

- `TestNewDialog_PathSelector_UpDownEscapesField_RegressionFor1020`
  (was: from S1 only) → rewrite as
  `TestPathField_ArrowsAlwaysEscapeUnlessPopupActive` (covers all
  reachable states).
- `TestNewDialog_PopupEnter_SelectsHighlightedSuggestion_RegressionFor896`
  → keep, but precondition changes from "type a prefix" to "press Enter
  to open popup".
- `TestNewDialog_PopupArrows_NavigateReliably_RegressionFor896`
  → keep, same precondition change.
- `TestNewDialog_TabAppliesSuggestionWhenNavigated` → still applies; the
  Tab autocomplete cycler is unchanged in Model A.

### 7.3 Manual + harness checks

- `tmux send-keys` script `/tmp/exec-ux-rethink-path/probe.sh` (regen-able)
  drives Name → Tab → Path → Enter → Down → Down → Enter → ... and asserts
  the captured pane text matches expected screenshots. Cheap; doable in
  CI under `-tags=ttyharness` (matches the host-sensitive pattern from
  PR #1019).

---

## 8. Decision points for review

1. Modal (A) vs Drawer (B)? RFC recommends A; B is on the table.
2. Drop `pathSoftSelected` entirely? Or keep the reverse-video render as
   a *cosmetic* highlight without behavioural meaning?
3. Apply the same model to the **model-ID popup** (`focusModel`,
   `modelSuggestionActive`) in the same PR? RFC recommends yes for
   consistency.
4. Tie the implementation to v1.10.0 or fast-follow on v1.9.15? RFC
   recommends v1.10.0 because the test migration is non-trivial.
5. Whose name on the PR? RFC author (TBD) suggests offering @JMBattista,
   @balazser, and @Showtimes prior-art credit; @MauriceDHanisch as
   parallel-track author for #885.

---

## 9. Appendix — code locations referenced

- `internal/ui/newdialog.go`
  - L179: `pathSoftSelected bool`
  - L1156–1188: `rebuildFocusTargets`
  - L1212–1254: `updateFocus`
  - L1299–1942: `Update(msg)` — main key dispatch
  - L1378–1387: #983 auto-activate gate (Up/Down opens popup when
    suggestions present)
  - L1478–1509: soft-select interception
  - L1696–1731: `case "enter"` — path/model popup activation; multi-repo
    edit toggle
  - L2355–2369: context-sensitive footer hints
- `internal/ui/home.go`
  - L6477: `h.newDialog.SetPathSuggestions(paths)` — feeder
- Regression tests
  - `internal/ui/issue1020_path_selector_ux_test.go`
  - `internal/ui/issue896_residual_test.go`
  - `internal/ui/newdialog_test.go` (TabAppliesSuggestionWhenNavigated,
    TypingResetsSuggestionNavigation, ...)
