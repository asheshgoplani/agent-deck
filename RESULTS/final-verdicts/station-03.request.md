ROLE
You are a consistency auditor. You distrust memory and labels. Build an explicit ledger from the artifacts.

EVIDENCE
Read all captured frames, the help overlay, README Quick Start, TUI Reference, and the machine-generated hotkey and glyph manifests. Do not read implementation rationale. Behavior observed in the fixture outranks prose.

TASK
Create a ledger with columns: concept, screen label, help label, documentation label, actual key, actual behavior, and status glyph. Check session states, group controls, create, attach, detach, restart, delete, archive, filter, cost, help, and settings. Check whether one key changes meaning because of runtime configuration. Check capitalization and vocabulary for the same action.

PASS BAR
PASS only if each user action has one stable name, each key has one meaning in a given context, each status has one glyph and plain-language label, and help plus documentation match observed behavior. A context change must be visibly named before a reused key is acceptable. Otherwise REJECT.

OUTPUT
VERDICT: PASS or REJECT
CONFLICTS: ledger rows with exact evidence, or NONE
UNEXPLAINED_GLYPHS: list or NONE
LARGEST_GAP: one conflict with the widest user impact
NEXT_FIX: one change only

REAL-USER COMPLAINT SEED
# Real-user complaint seed

- Issue #1975: [feature] Plugin system to cut the complexity — https://github.com/asheshgoplani/agent-deck/issues/1975
- Issue #1987: [bug] Local group header counts include archived sessions, so the count disagrees with the rows — https://github.com/asheshgoplani/agent-deck/issues/1987
- Issue #1866: Add timestamp to heartbeat/status update messages — https://github.com/asheshgoplani/agent-deck/issues/1866
- Issue #1950: notifications: add a notify-send backend for Linux desktop notifications (follow-up to #1893) — https://github.com/asheshgoplani/agent-deck/issues/1950
- Issue #1994: session show --json renders model as a family label ("GPT") instead of the stored value — https://github.com/asheshgoplani/agent-deck/issues/1994
- Issue #1703: feat: define and enforce a naming standard for session titles and groups — https://github.com/asheshgoplani/agent-deck/issues/1703
- Issue #1924: session set wrapper reports success without persisting; start failures leave last_error empty — https://github.com/asheshgoplani/agent-deck/issues/1924
- Issue #1978: session send reports "NOT delivered" for a message that was delivered and queued, whenever the target is busy and no submission signal is observed — https://github.com/asheshgoplani/agent-deck/issues/1978
- Issue #1554: session rename automatically — https://github.com/asheshgoplani/agent-deck/issues/1554
- Issue #1415: TUI: view modes to cycle group layout (active-on-top / populated-on-top) — https://github.com/asheshgoplani/agent-deck/issues/1415
- Issue #1131: bug - direct type still slow, still unresponsive, you can type a whole sentence before it appears — https://github.com/asheshgoplani/agent-deck/issues/1131
- Issue #1365: web: profile selector in Topbar is cosmetic — no server-side profile switching exists — https://github.com/asheshgoplani/agent-deck/issues/1365
- Issue #1701: launch -m sometimes never submits initial message; session registry can assign duplicate claude_session_id across concurrent launches — https://github.com/asheshgoplani/agent-deck/issues/1701
- Issue #896: Code path can be difficult to set due to obscured input — https://github.com/asheshgoplani/agent-deck/issues/896
- Issue #709: Launch TUI with a preselected session while keeping all groups visible — https://github.com/asheshgoplani/agent-deck/issues/709
- Issue #976: Autonomous ScheduleWakeup loop silently stalls for hours on upstream API 5xx — https://github.com/asheshgoplani/agent-deck/issues/976
- Issue #966: Bare slash commands ignored by freshly restarted child session via `session send` — https://github.com/asheshgoplani/agent-deck/issues/966
- Issue #1297: add: support a global `default_path` config key so the working directory doesn't have to be repeated every session — https://github.com/asheshgoplani/agent-deck/issues/1297
- Issue #1625: agent-deck force-sets `extended-keys on` / `extended-keys-format csi-u` on every spawn — user's `set -s extended-keys off` cannot win (Enter stops submitting) — https://github.com/asheshgoplani/agent-deck/issues/1625
- Issue #391: Feature: Per-session color/theme customization — https://github.com/asheshgoplani/agent-deck/issues/391
- Issue #960: `.mcp.json` plugin version pins go stale after plugin upgrade — https://github.com/asheshgoplani/agent-deck/issues/960
- Issue #680: Silent Telegram message loss: conductor group env_file leaks TELEGRAM_STATE_DIR to child sessions — https://github.com/asheshgoplani/agent-deck/issues/680
- Issue #876: agent-deck session send silently drops prompts in some conditions — https://github.com/asheshgoplani/agent-deck/issues/876
- Issue #479: session send --no-wait sends message twice — https://github.com/asheshgoplani/agent-deck/issues/479
- Issue #483: Feature: Search across session message history (not just titles) — https://github.com/asheshgoplani/agent-deck/issues/483
- Issue #211: Native session notification bridge, direct session→Slack/Telegram without conductor tokens — https://github.com/asheshgoplani/agent-deck/issues/211
- Issue #438: OpenClaw gateway integration: token auth sends wrong field + missing scopes for bridge — https://github.com/asheshgoplani/agent-deck/issues/438
- Issue #74: Can the claude code plugin be used in opencode? — https://github.com/asheshgoplani/agent-deck/issues/74
- Issue #607: v1.5.1 regression: TUI row offset drift when scrolling (all terminals) — https://github.com/asheshgoplani/agent-deck/issues/607
- Issue #5: light mode — https://github.com/asheshgoplani/agent-deck/issues/5
- Issue #222: [BUG] TOCTOU Race Condition in Worktree Path Validation — https://github.com/asheshgoplani/agent-deck/issues/222
- Issue #76: Loading indicator disappeared from session preview panel — https://github.com/asheshgoplani/agent-deck/issues/76
- Issue #48: Bug: Groups and sub groups behaving unexpectedly — https://github.com/asheshgoplani/agent-deck/issues/48
- Issue #33: Enhancement: Add opt-in "Docked Mode" for persistent sidebar during active sessions — https://github.com/asheshgoplani/agent-deck/issues/33
- Issue #20: Persistent error: "The tmux session no longer exists" — https://github.com/asheshgoplani/agent-deck/issues/20
- Discussion #609: Session back scrolling corrupted when I attach/re-attach, iterm2 — https://github.com/asheshgoplani/agent-deck/discussions/609

ARTIFACTS
# Real-user complaint seed

- Issue #1975: [feature] Plugin system to cut the complexity — https://github.com/asheshgoplani/agent-deck/issues/1975
- Issue #1987: [bug] Local group header counts include archived sessions, so the count disagrees with the rows — https://github.com/asheshgoplani/agent-deck/issues/1987
- Issue #1866: Add timestamp to heartbeat/status update messages — https://github.com/asheshgoplani/agent-deck/issues/1866
- Issue #1950: notifications: add a notify-send backend for Linux desktop notifications (follow-up to #1893) — https://github.com/asheshgoplani/agent-deck/issues/1950
- Issue #1994: session show --json renders model as a family label ("GPT") instead of the stored value — https://github.com/asheshgoplani/agent-deck/issues/1994
- Issue #1703: feat: define and enforce a naming standard for session titles and groups — https://github.com/asheshgoplani/agent-deck/issues/1703
- Issue #1924: session set wrapper reports success without persisting; start failures leave last_error empty — https://github.com/asheshgoplani/agent-deck/issues/1924
- Issue #1978: session send reports "NOT delivered" for a message that was delivered and queued, whenever the target is busy and no submission signal is observed — https://github.com/asheshgoplani/agent-deck/issues/1978
- Issue #1554: session rename automatically — https://github.com/asheshgoplani/agent-deck/issues/1554
- Issue #1415: TUI: view modes to cycle group layout (active-on-top / populated-on-top) — https://github.com/asheshgoplani/agent-deck/issues/1415
- Issue #1131: bug - direct type still slow, still unresponsive, you can type a whole sentence before it appears — https://github.com/asheshgoplani/agent-deck/issues/1131
- Issue #1365: web: profile selector in Topbar is cosmetic — no server-side profile switching exists — https://github.com/asheshgoplani/agent-deck/issues/1365
- Issue #1701: launch -m sometimes never submits initial message; session registry can assign duplicate claude_session_id across concurrent launches — https://github.com/asheshgoplani/agent-deck/issues/1701
- Issue #896: Code path can be difficult to set due to obscured input — https://github.com/asheshgoplani/agent-deck/issues/896
- Issue #709: Launch TUI with a preselected session while keeping all groups visible — https://github.com/asheshgoplani/agent-deck/issues/709
- Issue #976: Autonomous ScheduleWakeup loop silently stalls for hours on upstream API 5xx — https://github.com/asheshgoplani/agent-deck/issues/976
- Issue #966: Bare slash commands ignored by freshly restarted child session via `session send` — https://github.com/asheshgoplani/agent-deck/issues/966
- Issue #1297: add: support a global `default_path` config key so the working directory doesn't have to be repeated every session — https://github.com/asheshgoplani/agent-deck/issues/1297
- Issue #1625: agent-deck force-sets `extended-keys on` / `extended-keys-format csi-u` on every spawn — user's `set -s extended-keys off` cannot win (Enter stops submitting) — https://github.com/asheshgoplani/agent-deck/issues/1625
- Issue #391: Feature: Per-session color/theme customization — https://github.com/asheshgoplani/agent-deck/issues/391
- Issue #960: `.mcp.json` plugin version pins go stale after plugin upgrade — https://github.com/asheshgoplani/agent-deck/issues/960
- Issue #680: Silent Telegram message loss: conductor group env_file leaks TELEGRAM_STATE_DIR to child sessions — https://github.com/asheshgoplani/agent-deck/issues/680
- Issue #876: agent-deck session send silently drops prompts in some conditions — https://github.com/asheshgoplani/agent-deck/issues/876
- Issue #479: session send --no-wait sends message twice — https://github.com/asheshgoplani/agent-deck/issues/479
- Issue #483: Feature: Search across session message history (not just titles) — https://github.com/asheshgoplani/agent-deck/issues/483
- Issue #211: Native session notification bridge, direct session→Slack/Telegram without conductor tokens — https://github.com/asheshgoplani/agent-deck/issues/211
- Issue #438: OpenClaw gateway integration: token auth sends wrong field + missing scopes for bridge — https://github.com/asheshgoplani/agent-deck/issues/438
- Issue #74: Can the claude code plugin be used in opencode? — https://github.com/asheshgoplani/agent-deck/issues/74
- Issue #607: v1.5.1 regression: TUI row offset drift when scrolling (all terminals) — https://github.com/asheshgoplani/agent-deck/issues/607
- Issue #5: light mode — https://github.com/asheshgoplani/agent-deck/issues/5
- Issue #222: [BUG] TOCTOU Race Condition in Worktree Path Validation — https://github.com/asheshgoplani/agent-deck/issues/222
- Issue #76: Loading indicator disappeared from session preview panel — https://github.com/asheshgoplani/agent-deck/issues/76
- Issue #48: Bug: Groups and sub groups behaving unexpectedly — https://github.com/asheshgoplani/agent-deck/issues/48
- Issue #33: Enhancement: Add opt-in "Docked Mode" for persistent sidebar during active sessions — https://github.com/asheshgoplani/agent-deck/issues/33
- Issue #20: Persistent error: "The tmux session no longer exists" — https://github.com/asheshgoplani/agent-deck/issues/20
- Discussion #609: Session back scrolling corrupted when I attach/re-attach, iterm2 — https://github.com/asheshgoplani/agent-deck/discussions/609

# Captures

## FIXTURE_HOME.txt
```text
/tmp/agent-deck-gauntlet.VS81S2
```

## agents-100x30.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.0G/125.7G │ ▪ 195.9G/934.7G             v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                            │ PREVIEW
─────────────────────────────────── │ ──────────────────────────────────────────────────────────────
  ▾ platform (2)                    │ 📁 platform
   ├─ ✕ api-review shell            │
   └─ ✕ incident-shell shell        │ 2 sessions
2·▾ documentation (1)               │
   └─ ✕ release-notes shell         │ ✕ 2 error
                                    │
                                    │ ──────────────────────── Sessions ────────────────────────
                                    │   ✕ api-review shell
                                    │   ✕ incident-shell shell
                                    │
                                    │ Tab toggle • r rename • d delete • g subgroup
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete
```

## agents-160x48.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.1G/125.7G │ ▪ 195.7G/934.7G                                                                         v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                                                 │ PREVIEW
──────────────────────────────────────────────────────── │ ─────────────────────────────────────────────────────────────────────────────────────────────────────
  ▾ platform (2)                                         │ 📁 platform
   ├─ ✕ api-review shell                                 │
   └─ ✕ incident-shell shell                             │ 2 sessions
2·▾ documentation (1)                                    │
   └─ ✕ release-notes shell                              │ ✕ 2 error
                                                         │
                                                         │ ─────────────────────────────────────────── Sessions ───────────────────────────────────────────
                                                         │   ✕ api-review shell
                                                         │   ✕ incident-shell shell
                                                         │
                                                         │ Tab toggle • r rename • d delete • g subgroup
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete                   ↑↓ Nav │ +/- Move │ / Search │ G Global │ S Settings │ ? Help │ q Quit
```

## first-run-100x30.txt
```text



                   ╭────────────────────────────────────────────────────────────╮
                   │                                                            │
                   │                                                            │
                   │    [Welcome] > Tool > Claude > Ready                       │
                   │                                                            │
                   │    Welcome to Agent Deck!                                  │
                   │                                                            │
                   │                                                            │
                   │    Agent Deck is a terminal session manager for AI         │
                   │    coding agents.                                          │
                   │                                                            │
                   │    This wizard will help you configure:                    │
                   │      - Default AI tool                                     │
                   │      - Claude Code settings                                │
                   │                                                            │
                   │    Press Enter to continue or Esc to use defaults.         │
                   │                                                            │
                   │                                                            │
                   │                                                            │
                   │    Enter: continue                                         │
                   │                                                            │
                   │                                                            │
                   ╰────────────────────────────────────────────────────────────╯




```

## first-run-160x48.txt
```text












                                                 ╭────────────────────────────────────────────────────────────╮
                                                 │                                                            │
                                                 │                                                            │
                                                 │    [Welcome] > Tool > Claude > Ready                       │
                                                 │                                                            │
                                                 │    Welcome to Agent Deck!                                  │
                                                 │                                                            │
                                                 │                                                            │
                                                 │    Agent Deck is a terminal session manager for AI         │
                                                 │    coding agents.                                          │
                                                 │                                                            │
                                                 │    This wizard will help you configure:                    │
                                                 │      - Default AI tool                                     │
                                                 │      - Claude Code settings                                │
                                                 │                                                            │
                                                 │    Press Enter to continue or Esc to use defaults.         │
                                                 │                                                            │
                                                 │                                                            │
                                                 │                                                            │
                                                 │    Enter: continue                                         │
                                                 │                                                            │
                                                 │                                                            │
                                                 ╰────────────────────────────────────────────────────────────╯













```

## help-100x30.txt
```text
         ╭────────────────────────────────────────────────────────────────────────────────╮
         │                                                                                │
         │  KEYBOARD SHORTCUTS                                                            │
         │                                                                                │
         │  QUICK START                                                                   │
         │    Enter         Attach to selected session                                    │
         │    R             Restart selected session                                      │
         │    Ctrl+Q        Detach from session                                           │
         │    ?             Open this help                                                │
         │                                                                                │
         │  NAVIGATION                                                                    │
         │    j / Down      Move down                                                     │
         │    k / Up        Move up                                                       │
         │    Ctrl+u/d      Half page up/down                                             │
         │    PgUp / PgDn   Half page up/down                                             │
         │    Ctrl+f/b      Full page up/down                                             │
         │    Home / End    Jump to first / last item                                     │
         │    gg / G        Jump to top / global search                                   │
         │    h / Left      Collapse / parent                                             │
         │    l / Right     Expand / toggle                                               │
         │    1-9           Jump to root group                                            │
         │    Space         Jump mode                                                     │
         │    Enter         Attach / toggle                                               │
         │    Shift+Enter   Open session in new iTerm window (macOS)                      │
         │  ▼ more below                                                                  │
         │                                                                                │
         │  j/k scroll • any other key to close                                           │
         │                                                                                │
         ╰────────────────────────────────────────────────────────────────────────────────╯

```

## help-160x48.txt
```text
                                       ╭────────────────────────────────────────────────────────────────────────────────╮
                                       │                                                                                │
                                       │  KEYBOARD SHORTCUTS                                                            │
                                       │                                                                                │
                                       │  QUICK START                                                                   │
                                       │    Enter         Attach to selected session                                    │
                                       │    R             Restart selected session                                      │
                                       │    Ctrl+Q        Detach from session                                           │
                                       │    ?             Open this help                                                │
                                       │                                                                                │
                                       │  NAVIGATION                                                                    │
                                       │    j / Down      Move down                                                     │
                                       │    k / Up        Move up                                                       │
                                       │    Ctrl+u/d      Half page up/down                                             │
                                       │    PgUp / PgDn   Half page up/down                                             │
                                       │    Ctrl+f/b      Full page up/down                                             │
                                       │    Home / End    Jump to first / last item                                     │
                                       │    gg / G        Jump to top / global search                                   │
                                       │    h / Left      Collapse / parent                                             │
                                       │    l / Right     Expand / toggle                                               │
                                       │    1-9           Jump to root group                                            │
                                       │    Space         Jump mode                                                     │
                                       │    Enter         Attach / toggle                                               │
                                       │    Shift+Enter   Open session in new iTerm window (macOS)                      │
                                       │                                                                                │
                                       │  GROUP NAVIGATION (v1.7.60)                                                    │
                                       │    Alt+j / Alt+k Next / prev session in group                                  │
                                       │    Alt+1 - Alt+9 Jump to Nth session in group                                  │
                                       │    Alt+g / Alt+G First / last in group                                         │
                                       │    Alt+/         Filter search in group                                        │
                                       │                                                                                │
                                       │  SESSIONS                                                                      │
                                       │    n/N           New / quick create                                            │
                                       │    r             Rename session                                                │
                                       │    R             Restart session                                               │
                                       │    T             Restart with new session ID                                   │
                                       │    d             Delete session                                                │
                                       │    D             Close session process                                         │
                                       │    ctrl+z        Undo delete                                                   │
                                       │    A             Archive session                                               │
                                       │    shift+u       Unarchive session                                             │
                                       │    ^             Toggle archived view                                          │
                                       │  ▼ more below                                                                  │
                                       │                                                                                │
                                       │  j/k scroll • any other key to close                                           │
                                       │                                                                                │
                                       ╰────────────────────────────────────────────────────────────────────────────────╯

```

## main-list-100x30.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.0G/125.7G │ ▪ 195.7G/934.7G             v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                            │ PREVIEW
─────────────────────────────────── │ ──────────────────────────────────────────────────────────────
  ▾ platform (2)                    │ 📁 platform
   ├─ ✕ api-review shell            │
   └─ ✕ incident-shell shell        │ 2 sessions
2·▾ documentation (1)               │
   └─ ✕ release-notes shell         │ ✕ 2 error
                                    │
                                    │ ──────────────────────── Sessions ────────────────────────
                                    │   ✕ api-review shell
                                    │   ✕ incident-shell shell
                                    │
                                    │ Tab toggle • r rename • d delete • g subgroup
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete
```

## main-list-160x48.txt
```text













                                                      ╭──────────────────────────────────────────────────╮
                                                      │                                                  │
                                                      │  Claude Code Hooks                               │
                                                      │                                                  │
                                                      │  Agent-deck can install Claude Code lifecycle    │
                                                      │  hooks                                           │
                                                      │  for real-time status detection (instant         │
                                                      │  green/yellow/gray).                             │
                                                      │                                                  │
                                                      │  This writes to your Claude settings.json        │
                                                      │  (preserves existing settings).                  │
                                                      │  New/restarted sessions will use hooks;          │
                                                      │  existing sessions continue unchanged.           │
                                                      │  You can disable later with: hooks_enabled =     │
                                                      │  false in config.toml                            │
                                                      │                                                  │
                                                      │                                                  │
                                                      │      Install      ▸ Skip                         │
                                                      │  y install · n skip · ←/→ navigate · Enter       │
                                                      │  select · Esc                                    │
                                                      │                                                  │
                                                      ╰──────────────────────────────────────────────────╯













```

## model-picker-100x30.txt
```text
       │      cursor agent    hermes    deepseek                                            │
       │                                                                                    │
       │      Model ID:                                                                     │
       │      > claude-fable-5                                                              │
       │      Examples: claude-opus-5, claude-sonnet-5, claude-haiku-4-5                    │
       │                                                                                    │
       │    ▶ Reasoning effort: ← Tool default →                                            │
       │                                                                                    │
       │      Path:                                                                         │
       │      > /tmp/agent-deck-gauntlet.VS81S2/fixture-home/projects/api                   │
       │                                                                                    │
       │      [ ] Create in worktree                                                        │
       │      [ ] Run in Docker sandbox                                                     │
       │                                                                                    │
       │      [ ] Multi-repo mode                                                           │
       │                                                                                    │
       │    ─ Claude Options ─                                                              │
       │      Session: (•) New  ( ) Continue  ( ) Resume                                    │
       │      [ ] Skip permissions                                                          │
       │      [ ] Auto mode                                                                 │
       │      [ ] Chrome mode                                                               │
       │      [ ] Teammate mode                                                             │
       │        Extra args: > --agent reviewer --model opus                                 │
       │        Start query: > initial prompt (not split on spaces)                         │
       │                                                                                    │
       │                                                                                    │
       │    ←→/Space choose effort │ Tab next │ Enter/^S create │ Esc cancel                │
       │                                                                                    │
       │                                                                                    │
       ╰────────────────────────────────────────────────────────────────────────────────────╯
```

## model-picker-160x48.txt
```text


                                     ╭────────────────────────────────────────────────────────────────────────────────────╮
                                     │                                                                                    │
                                     │                                                                                    │
                                     │    New Session                                                                     │
                                     │                                                                                    │
                                     │      in group: platform                                                            │
                                     │                                                                                    │
                                     │      Name:                                                                         │
                                     │      > session-name                                                                │
                                     │                                                                                    │
                                     │      Command:                                                                      │
                                     │                                                                                    │
                                     │      shell    claude    gemini    opencode    codex    pi    copilot    crush      │
                                     │      cursor agent    hermes    deepseek                                            │
                                     │                                                                                    │
                                     │      Model ID:                                                                     │
                                     │      > claude-fable-5                                                              │
                                     │      Examples: claude-opus-5, claude-sonnet-5, claude-haiku-4-5                    │
                                     │                                                                                    │
                                     │    ▶ Reasoning effort: ← Tool default →                                            │
                                     │                                                                                    │
                                     │      Path:                                                                         │
                                     │      > /tmp/agent-deck-gauntlet.VS81S2/fixture-home/projects/api                   │
                                     │                                                                                    │
                                     │      [ ] Create in worktree                                                        │
                                     │      [ ] Run in Docker sandbox                                                     │
                                     │                                                                                    │
                                     │      [ ] Multi-repo mode                                                           │
                                     │                                                                                    │
                                     │    ─ Claude Options ─                                                              │
                                     │      Session: (•) New  ( ) Continue  ( ) Resume                                    │
                                     │      [ ] Skip permissions                                                          │
                                     │      [ ] Auto mode                                                                 │
                                     │      [ ] Chrome mode                                                               │
                                     │      [ ] Teammate mode                                                             │
                                     │        Extra args: > --agent reviewer --model opus                                 │
                                     │        Start query: > initial prompt (not split on spaces)                         │
                                     │                                                                                    │
                                     │                                                                                    │
                                     │    ←→/Space choose effort │ Tab next │ Enter/^S create │ Esc cancel                │
                                     │                                                                                    │
                                     │                                                                                    │
                                     ╰────────────────────────────────────────────────────────────────────────────────────╯



```

## new-session-100x30.txt
```text
       │      cursor agent    hermes    deepseek                                            │
       │                                                                                    │
       │      Model ID:                                                                     │
       │      > claude-sonnet-4-6                                                           │
       │      Examples: claude-opus-5, claude-sonnet-5, claude-haiku-4-5                    │
       │                                                                                    │
       │      Reasoning effort: Tool default                                                │
       │                                                                                    │
       │      Path:                                                                         │
       │      > /tmp/agent-deck-gauntlet.VS81S2/fixture-home/projects/api                   │
       │                                                                                    │
       │      [ ] Create in worktree                                                        │
       │      [ ] Run in Docker sandbox                                                     │
       │                                                                                    │
       │      [ ] Multi-repo mode                                                           │
       │                                                                                    │
       │    ─ Claude Options ─                                                              │
       │      Session: (•) New  ( ) Continue  ( ) Resume                                    │
       │      [ ] Skip permissions                                                          │
       │      [ ] Auto mode                                                                 │
       │      [ ] Chrome mode                                                               │
       │      [ ] Teammate mode                                                             │
       │        Extra args: > --agent reviewer --model opus                                 │
       │        Start query: > initial prompt (not split on spaces)                         │
       │                                                                                    │
       │                                                                                    │
       │    Tab next │ ↑↓ navigate │ ^S create │ Esc cancel                                 │
       │                                                                                    │
       │                                                                                    │
       ╰────────────────────────────────────────────────────────────────────────────────────╯
```

## new-session-160x48.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.1G/125.7G │ ▪ 195.7G/934.7G                                                                         v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                                                 │ PREVIEW
──────────────────────────────────────────────────────── │ ─────────────────────────────────────────────────────────────────────────────────────────────────────
  ▾ platform (2)                                         │ 📁 platform
   ├─ ✕ api-review shell                                 │
   └─ ✕ incident-shell shell                             │ 2 sessions
2·▾ documentation (1)                                    │
   └─ ✕ release-notes shell                              │ ✕ 2 error
                                                         │
                                                         │ ─────────────────────────────────────────── Sessions ───────────────────────────────────────────
                                                         │   ✕ api-review shell
                                                         │   ✕ incident-shell shell
                                                         │
                                                         │ Tab toggle • r rename • d delete • g subgroup
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete                   ↑↓ Nav │ +/- Move │ / Search │ G Global │ S Settings │ ? Help │ q Quit
```

## preview-100x30.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.1G/125.7G │ ▪ 195.7G/934.7G             v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                            │ PREVIEW
─────────────────────────────────── │ ──────────────────────────────────────────────────────────────
  ▾ platform (2)                    │ 📁 platform
   ├─ ✕ api-review shell            │
   └─ ✕ incident-shell shell        │ 2 sessions
2·▾ documentation (1)               │
   └─ ✕ release-notes shell         │ ✕ 2 error
                                    │
                                    │ ──────────────────────── Sessions ────────────────────────
                                    │   ✕ api-review shell
                                    │   ✕ incident-shell shell
                                    │
                                    │ Tab toggle • r rename • d delete • g subgroup
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete
```

## preview-160x48.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.1G/125.7G │ ▪ 195.7G/934.7G                                                                         v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                                                 │ PREVIEW
──────────────────────────────────────────────────────── │ ─────────────────────────────────────────────────────────────────────────────────────────────────────
  ▾ platform (2)                                         │ 📁 platform
   ├─ ✕ api-review shell                                 │
   └─ ✕ incident-shell shell                             │ 2 sessions
2·▾ documentation (1)                                    │
   └─ ✕ release-notes shell                              │ ✕ 2 error
                                                         │
                                                         │ ─────────────────────────────────────────── Sessions ───────────────────────────────────────────
                                                         │   ✕ api-review shell
                                                         │   ✕ incident-shell shell
                                                         │
                                                         │ Tab toggle • r rename • d delete • g subgroup
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Group:  Tab  Toggle  n/N  New/Quick  g  Group │  r  Rename  d  Delete                   ↑↓ Nav │ +/- Move │ / Search │ G Global │ S Settings │ ? Help │ q Quit
```

## skills-100x30.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 21.9G/125.7G │ ▪ 195.9G/934.7G             v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                            │ PREVIEW
─────────────────────────────────── │ ──────────────────────────────────────────────────────────────
1·▾ platform (2)                    │ api-review  ✕ error
  ▶├─ ✕ api-review shell            │ 📁 /tmp/agent-deck-gauntlet.VS81S2/fixture-home/projects/api
   └─ ✕ incident-shell shell        │ ⏱ just now
2·▾ documentation (1)               │  shell   platform
   └─ ✕ release-notes shell         │
                                    │ ───────────────────── Session Error ─────────────────────
                                    │
                                    │ ✕ No tmux session running   R Restart
                                    │
                                    │ This can happen if:
                                    │   - Session was added but not yet started
                                    │   - tmux server was restarted
                                    │   - Terminal was closed or system rebooted
                                    │
                                    │ Actions:
                                    │   d Delete  - remove from list
                                    │   Enter - attach (will auto-start)
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
                                    │
────────────────────────────────────────────────────────────────────────────────────────────────────
Session:  Enter  Attach  n/N  New/Quick  g  Group  R  Restart  H  Shell  c  Copy  V  Copy pane  x  S
```

## skills-160x48.txt
```text
 ⟨ ○ │ ○ │ ○ ⟩  Agent Deck  ✕ 3 error • ⚙ 0% │ ⛁ 22.1G/125.7G │ ▪ 195.7G/934.7G                                                                         v1.14.0
  All    ● 0   ◐ 0   ○ 0   ✕ 3   !@#& filter • 0 all • % open • ^ archived • t view
SESSIONS                                                 │ PREVIEW
──────────────────────────────────────────────────────── │ ─────────────────────────────────────────────────────────────────────────────────────────────────────
1·▾ platform (2)                                         │ api-review  ✕ error
  ▶├─ ✕ api-review shell                                 │ 📁 /tmp/agent-deck-gauntlet.VS81S2/fixture-home/projects/api
   └─ ✕ incident-shell shell                             │ ⏱ just now
2·▾ documentation (1)                                    │  shell   platform
   └─ ✕ release-notes shell                              │
                                                         │ ───────────────────────────────────────── Session Error ─────────────────────────────────────────
                                                         │
                                                         │ ✕ No tmux session running   R Restart
                                                         │
                                                         │ This can happen if:
                                                         │   - Session was added but not yet started
                                                         │   - tmux server was restarted
                                                         │   - Terminal was closed or system rebooted
                                                         │
                                                         │ Actions:
                                                         │   d Delete  - remove from list
                                                         │   Enter - attach (will auto-start)
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
                                                         │
────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────────
Session:  Enter  Attach  n/N  New/Quick  g  Group  R  Restart  H  Shell  c  Copy  V  Copy pane  x  Send │  r  Rename  M  Move  d  Delete  D  Close
```

ROTATION OUTPUT CONTRACT
Aspect: core. Screens: first-run main-list new-session help preview
Add REASON and SEVERITY (BLOCKER, MAJOR, MINOR, or NONE). For every named screen and each size, add exactly: SCREEN_VERDICT: <screen> <160x48|100x30> <PASS|REJECT>. Missing evidence must be REJECT.
