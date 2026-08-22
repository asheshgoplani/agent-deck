VERDICT: REJECT

REASON: Recovery evidence is incomplete, and the shortcut/status vocabulary is internally inconsistent. Most seriously, `$` changes from “error filter” to “Cost Dashboard” when cost tracking is available, without a visibly named mode change.

SEVERITY: BLOCKER

CONFLICTS:

| concept | screen label | help label | documentation label | actual key | actual behavior | status glyph |
|---|---|---|---|---|---|---|
| Session states | Header: `● 0`, `◐ 0`, `○ 0`, `✕ 3`; preview: `✕ error` | No status legend in captured help | TUI: Running, Waiting, Idle, Error, Starting | N/A | Generated glyph mapping also defines Stopped, model unavailable, and authentication failure | Screen/docs: `● ◐ ○ ✕ ⟳`; generated mapping additionally has `■ ⚡ 🔒` |
| Group toggle | Footer: `Tab Toggle`; preview: `Tab toggle` | `Enter Attach / toggle`; `l / Right Expand / toggle` | `Enter Attach to session OR toggle group`; `Tab Toggle expand/collapse group` | `Enter`, `Tab`, `l`, `Right` | Selected session attaches; selected group toggles | N/A |
| Create | Footer: `n/N New/Quick`; dialog alternates between `Enter/^S create` and `^S create` | `n/N New / quick create` | README: `n` New session; TUI: `Ctrl+S` create, while Enter’s behavior depends on `[ui].new_session_enter_advances` | `n`, `N`, `Ctrl+S`, conditionally `Enter` | `n` opens dialog; `N` quick-creates; Enter either advances or submits according to runtime configuration | N/A |
| Attach | Footer: `Enter Attach`; error preview: `Enter - attach (will auto-start)` | `Enter Attach to selected session` and later `Attach / toggle` | `Enter Attach to session OR toggle group` | `Enter` | Attaches a session, auto-starting it if its tmux pane is absent; toggles a group in group context | N/A |
| Detach | Not visible on main screen | `Ctrl+Q Detach from session` | TUI: `Ctrl+Q Detach (keep tmux running)` | Default `Ctrl+Q`, but rebindable | Detaches while preserving tmux | N/A |
| Restart | Error preview: `R Restart`; footer: `R Restart` | `R Restart session`; `T Restart with new session ID` | README/TUI: `R Restart session` | `R`; fresh restart `T` | Restarts the selected session; `T` changes its session ID | N/A |
| Delete | Footer/preview: `d Delete` | `d Delete session`; `Ctrl+Z Undo delete` | TUI says Delete; README CLI Quick Start says `session remove` and CLI says Remove from registry | `d` | Deletes/removes a session from the registry after confirmation | N/A |
| Archive | Header: `^ archived` | `A Archive`, `shift+u Unarchive`, `^ Toggle archived view` | README uses `A / Shift+U` and `^ Show archived sessions`; TUI says `^ Filter: view archived sessions (toggle)` | `A`, `Shift+U`, `^` | Archives, unarchives, or replaces the normal list with archived sessions | Archived mapping uses undocumented `■` |
| Filter | Captured header: `!@#& filter • 0 all • % open • ^ archived` | Captured help omits individual status-filter keys | TUI: `! @ # $` for running/waiting/idle/error | `!`, `@`, `#`, implementation `$`; screen advertises `&` | Status filtering uses `$` for errors only when cost tracking is unavailable | `● ◐ ○ ✕` |
| Cost | No explicit main-screen label | `$ Cost Dashboard` | README: `$ Cost Dashboard`; TUI instead documents `$ Filter: error only` | `$` | Opens Cost Dashboard when a cost store exists; otherwise filters errors | N/A |
| Help | Footer: `? Help` | `? Open this help` / `This help` | README: `? Full help`; TUI: `? Help overlay` | `?` | Opens help | N/A |
| Settings | Footer: `S Settings` | `S Settings` | README: `S Settings` | `S` | Opens settings | N/A |
| Recovery: missing tmux session | Captured session preview: `✕ No tmux session running`, `R Restart`, `d Delete`, `Enter - attach (will auto-start)` | `R Restart selected session` | TUI only defines restart/attach; no recovery procedure | `R`, `d`, `Enter` | Gives actionable in-TUI recovery, but no concrete shell command | `✕` |
| Recovery: tmux executable absent | Only injection text: `Injected scenario: tmux executable absent. Judge recovery copy and concrete next command.` | No evidence | No captured recovery copy | Unknown | No rendered behavior or next command supplied | Unknown |
| Recovery: selected AI executable absent | Only injection text: `Injected scenario: selected AI executable absent. Judge recovery copy and concrete next command.` | No evidence | No captured recovery copy | Unknown | No rendered behavior or next command supplied | Unknown |

UNEXPLAINED_GLYPHS: `■` stopped/archived, `⚡` model unavailable, and `🔒` authentication failure exist in the generated glyph mapping but are absent from the TUI status reference. Captured system glyphs `⚙`, `⛁`, and `▪` also lack plain-language labels.

LARGEST_GAP: `$` has two runtime-dependent meanings—Cost Dashboard or error filter—while the visible header advertises `&` for filtering, help advertises `$` only for cost, and the TUI reference advertises `$` only for errors. No visibly named context explains the switch.

NEXT_FIX: Give error filtering a dedicated stable key and update the header, help overlay, README, TUI reference, and generated hotkey manifest to use it; reserve `$` exclusively for Cost Dashboard.

SCREEN_VERDICT: error-preview 160x48 REJECT  
SCREEN_VERDICT: error-preview 100x30 REJECT  
SCREEN_VERDICT: missing-tmux-session 160x48 REJECT  
SCREEN_VERDICT: missing-tmux-session 100x30 REJECT  
SCREEN_VERDICT: missing-tmux-executable 160x48 REJECT  
SCREEN_VERDICT: missing-tmux-executable 100x30 REJECT  
SCREEN_VERDICT: missing-tool 160x48 REJECT  
SCREEN_VERDICT: missing-tool 100x30 REJECT