VERDICT: REJECT

REASON: `$` changes from error filtering to Cost Dashboard when runtime cost tracking is active, without any visibly named mode change. Documentation, help, and the captured status bar also disagree on filter keys and action vocabulary. Several required screens are missing or captured in the wrong state.

SEVERITY: BLOCKER

CONFLICTS:

| concept | screen label | help label | documentation label | actual key | actual behavior | status glyph |
|---|---|---|---|---|---|---|
| Error filter / cost | Main header: `!@#& filter`; README: `$ Cost Dashboard` | `$ Cost Dashboard` | TUI Reference: `$` = `Filter: error only (toggle)`; README: `$` = `Cost Dashboard` | `$`; captured header instead advertises `&` | `$` opens Cost Dashboard when cost tracking is active, but filters errors when inactive. No visible mode name precedes this reuse. | `✕ error` |
| Create session | Footer: `n/N New/Quick`; dialog: `^S create`; model-picker capture: `Enter/^S create` | `n/N New / quick create` | README: `n New session`; TUI Reference: `n New session`; `Ctrl+S create` | `n`, `N`, `Ctrl+S`, configuration-dependent `Enter` | `n` opens the dialog; `N` quick-creates; `Ctrl+S` creates. With `[ui].new_session_enter_advances=true`, Enter advances on text fields; with false, Enter creates. Footer does not name the configured mode. | — |
| Group creation | Main footer: `g Group`; preview: `g subgroup` | `g New group` | `g Create group (subgroup if on group)` | `g` | Creates a root group or subgroup according to selection context, but the main footer merely says `Group`; capitalization/vocabulary vary among `Group`, `group`, `New group`, `Create group`, and `subgroup`. | `📁` appears for group, without a glyph legend |
| Attach | Session footer: `Enter Attach`; error preview: `Enter - attach (will auto-start)` | `Attach to selected session`; navigation says `Attach / toggle` | README: `Attach to session`; TUI Reference: `Attach to session OR toggle group` | `Enter` | Attaches and may auto-start an errored session; toggles a selected group. Context is selection-dependent, but help’s Quick Start omits toggle and README only says attach. | Selected fixture session is `✕ error` |
| Detach | Not visible on captured main/session footer | `Ctrl+Q Detach from session` | TUI Reference: `Ctrl+Q Detach (keep tmux running)` | Configurable detach chord; default `Ctrl+Q` | Detaches while attached. Help capture hard-codes `Ctrl+Q`, although runtime configuration can change the detach chord. | — |
| Restart | Error preview/footer: `R Restart` | `R Restart session`; `T Restart with new session ID` | README/TUI Reference: `R Restart session (reloads MCPs)` | `R`, `T` | `R` restarts; `T` restarts with a fresh session ID. README Quick Start omits `T`; vocabulary differs between “new” and “fresh” session ID sources. | `✕` before restart; no observed post-restart state |
| Delete | Preview: `d Delete - remove from list`; group footer: `d Delete` | `d Delete session`; `D Close session process`; `Ctrl+Z Undo delete` | TUI Reference: delete confirmation warns of tmux kill/process termination; group sessions move to default | `d` | Meaning varies materially by selection: delete session versus delete group; the error preview describes only “remove from list,” while documentation describes process termination. | — |
| Archive | Header: `^ archived` | `A Archive session`; `Shift+U Unarchive`; `^ Toggle archived view` | README/TUI Reference match these names | `A`, `Shift+U`, `^` | Required behavior is not observed in any captured frame. The generated glyph mapping uses `■` for both stopped and archived. | `■` means both `Stopped` and `Archived` |
| Filter | Header: `0 all • % open • ^ archived`; also `!@#& filter` | Captured help portion does not expose the complete filter mapping | TUI Reference: `! running`, `@ waiting`, `# idle`, `$ error`, `0 all`, `^ archived` | Captured error key `&`; documented error key `$` | Captured UI and documentation disagree; `$` is additionally runtime-dependent. | `●`, `◐`, `○`, `✕` |
| Help | Footer: `? Help` | `? Open this help` / `This help` | README: `? Full help`; TUI Reference: `? Help overlay` | `?` | Opens help, but the 100×30 capture truncates required actions and both captures require scrolling; captured evidence does not establish complete parity. | — |
| Settings | Footer: `S Settings` | Settings lies below the visible portion of the captured overlay | README/TUI Reference: `S Settings` | `S` | Main-screen behavior is advertised, but no settings screen or observed result was captured. | `⚙ 0%` appears in the header without a plain-language legend |
| Session states | Header visibly labels `✕ 3 error`; TUI Reference lists Running, Waiting, Idle, Error, Starting | No complete status legend in captured help | TUI Reference: `● Running`, `◐ Waiting`, `○ Idle`, `✕ Error`, `⟳ Starting` | Status-driven | Fixture observes only error. Generated glyph mapping additionally emits `■`, `⚡`, and `🔒`, which the TUI Reference status table does not explain. `■` is reused for stopped and archived. | `●`, `◐`, `○`, `✕`, `⟳`, `■`, `⚡`, `🔒` |

UNEXPLAINED_GLYPHS: `■` (ambiguous: stopped or archived), `⚡` (model unavailable), `🔒` (authentication required), plus unlegendized screen glyphs `⚙`, `⛁`, `▪`, `📁`, and `⏱`.

LARGEST_GAP: `$` has fleet-wide impact because it changes between “Cost Dashboard” and “filter errors” based on runtime cost availability, while the captured header advertises `&` and the TUI Reference still documents `$` as the error filter. The user cannot predict the result from the visible context.

NEXT_FIX: Reserve `$` exclusively for Cost Dashboard and standardize `&` as the error-filter key across dispatch, header, help, README Quick Start, and TUI Reference.

SCREEN_VERDICT: first-run 160x48 PASS  
SCREEN_VERDICT: first-run 100x30 PASS  
SCREEN_VERDICT: main-list 160x48 REJECT  
SCREEN_VERDICT: main-list 100x30 PASS  
SCREEN_VERDICT: new-session 160x48 REJECT  
SCREEN_VERDICT: new-session 100x30 PASS  
SCREEN_VERDICT: help 160x48 REJECT  
SCREEN_VERDICT: help 100x30 REJECT  
SCREEN_VERDICT: preview 160x48 PASS  
SCREEN_VERDICT: preview 100x30 PASS