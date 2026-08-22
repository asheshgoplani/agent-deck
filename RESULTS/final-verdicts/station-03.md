VERDICT: REJECT

REASON: The observed UI, help, README Quick Start, and TUI Reference do not maintain one stable key/name mapping. Most seriously, `$` is documented for two same-context actions—Cost Dashboard and error filtering—while the captured screen advertises `&` for the error filter. Several required captures are also missing or show the wrong screen.

SEVERITY: BLOCKER

CONFLICTS:

| concept | screen label | help label | documentation label | actual key | actual behavior | status glyph |
|---|---|---|---|---|---|---|
| Error filter | `!@#& filter` | Not visible in captured help | TUI Reference: `$` “Filter: error only” | Screen: `&`; docs: `$` | Filters the list to error sessions | `✕` |
| Cost | Not visible | Not visible in captured help | README: `$` “Cost Dashboard” | `$` | Opens Cost Dashboard | N/A |
| Create session | Footer: `n/N New/Quick`; dialog: `^S create`; alternate capture: `Enter/^S create` | `n/N New / quick create` | README: `n` “New session”; TUI Reference: `n` “New session” | `n`, `N`, `Ctrl+S`, and configuration-dependent `Enter` | `n` opens dialog; `N` quick-creates; `Ctrl+S` creates; `Enter` either advances or creates depending on `[ui].new_session_enter_advances` | N/A |
| Attach | `Enter Attach`; error preview: `Enter - attach (will auto-start)` | `Enter Attach to selected session`; elsewhere `Enter Attach / toggle` | README: “Attach to session”; TUI Reference: “Attach to session OR toggle group” | `Enter` | On an errored session, auto-starts and attaches; on a group, toggles expansion | N/A |
| Detach | No captured attached-session label | `Ctrl+Q Detach from session` | TUI Reference: `Ctrl+Q` “Detach (keep tmux running)” | `Ctrl+Q` | Detaches while leaving tmux running | N/A |
| Restart | `R Restart` | `R Restart selected session`; `R Restart session` | README/TUI Reference: “Restart session” | `R` | Restarts selected session | `⟳` documented for Starting, but not observed |
| Delete | `d Delete`; preview says “remove from list” | `d Delete session` | README: “Delete”; TUI Reference warns of tmux kill/process termination | `d` | Behavior varies by state: errored-session preview promises registry removal, while documentation describes process termination | N/A |
| Archive | `^ archived` only; no archive-action label | `A Archive session`, `shift+u Unarchive session` | README/TUI Reference: Archive / unarchive | `A`, `Shift+U`, `^` | Archive stops tmux and hides session; unarchive restores it; `^` changes archived view | N/A |
| Group controls | `Tab Toggle`, `g Group`; preview: `g subgroup` | `Enter Attach / toggle` | TUI Reference: `g Create group (subgroup if on group)` | `Tab`, `Enter`, `g` | Toggle group expansion or create a group/subgroup based on selection | `▾` expanded; plain-language glyph legend absent |
| Settings | `S Settings` at 160×48; absent at 100×30 | Not visible in captured help | README: `S Settings` | `S` | Opens Settings | `⚙` appears in header but is not explained as status versus resource indicator |
| Help | `? Help` at 160×48; absent at 100×30 | `? Open this help` | README/TUI Reference: Help / Help overlay | `?` | Opens help overlay | N/A |
| Session states | Header/list: `●`, `◐`, `○`, `✕`; observed rows are `✕ … shell`; preview says `✕ error` | No captured status legend | README/TUI Reference: Running, Waiting, Idle, Error; TUI Reference additionally lists Starting | N/A | Fixture shows three errored sessions because no tmux session exists | `●` Running; `◐` Waiting; `○` Idle; `✕` Error; `⟳` Starting |

UNEXPLAINED_GLYPHS: `⚙`, `⛁`, `▪`, `📁`, `⏱`, `▶`, `▾`, and the leading `2·` group marker lack plain-language explanations in the supplied help/documentation evidence. `⟳` is documented but not observed in any captured frame.

LARGEST_GAP: `$` has two documented meanings in the same list context—Cost Dashboard and error-only filtering—while the actual screen advertises `&` for filtering. Users cannot reliably learn the filter key from documentation.

NEXT_FIX: Change the TUI Reference’s error-filter row from `$` to `&`, preserving `$` exclusively for Cost Dashboard.

SCREEN_VERDICT: first-run 160x48 PASS  
SCREEN_VERDICT: first-run 100x30 PASS  
SCREEN_VERDICT: main-list 160x48 REJECT  
SCREEN_VERDICT: main-list 100x30 PASS  
SCREEN_VERDICT: new-session 160x48 REJECT  
SCREEN_VERDICT: new-session 100x30 REJECT  
SCREEN_VERDICT: help 160x48 REJECT  
SCREEN_VERDICT: help 100x30 REJECT  
SCREEN_VERDICT: preview 160x48 PASS  
SCREEN_VERDICT: preview 100x30 PASS