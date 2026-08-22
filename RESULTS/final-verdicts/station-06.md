VERDICT: REJECT

REASON: The required happy path could not complete: `/fixture/project` does not exist. Recovery scenarios also lack executable evidence showing the user-facing failures and next commands.

SEVERITY: BLOCKER

STEPS:
1. Read README Quick Start: `agent-deck` launches the TUI; elapsed 0s.
2. Ran `agent-deck` with fixture-owned HOME/XDG/PATH and no host tmux server; welcome wizard appeared; elapsed 3s.
3. Pressed `Esc`; reached a useful empty state showing “No Sessions Yet,” `n` to create, and `i` to import; elapsed 6s.
4. Pressed `n`; New Session opened with `shell` selected. Its path defaulted to `/home/ashesh`, not `/fixture/project`; elapsed 7s.
5. Checked `/fixture/project`; visible result: `No such file or directory`; elapsed 8s. Shell creation, attach, and detach could not be safely completed.
6. Located help through `?`; it explicitly shows `Enter Attach`, `Ctrl+Q Detach`, and `n/N New / quick create`; elapsed 9s.
7. Identified AI-session creation: press `n`, select `claude`, `codex`, or another listed tool, set fields, then `Ctrl+S`; elapsed 10s.
8. Injected missing-tmux evidence contained only the scenario expectation, not an observed failure. Source code promises “Error: tmux not found” plus `sudo apt install tmux`, but this was not demonstrated; elapsed 12s.
9. Injected missing-tool evidence likewise contained no observed failure or concrete recovery command; elapsed 13s.

DOC_DRIFT:
- README: `agent-deck add . -c claude        # Add current dir with Claude`
  Observed: the required current directory `/fixture/project` does not exist, so this walkthrough command cannot target the specified project.
- README Key Shortcuts includes `Enter | Attach to session` but contains no detach row.
  Observed help: `Ctrl+Q        Detach from session`.
- README: `n | New session`.
  Observed: creation requires `Ctrl+S`; this is explained by the nearby v1.9.55 warning, but not by the shortcut table itself.

RECOVERY_GAPS:
- Missing tmux: supplied artifact only says “Expected evidence must name tmux and give a concrete recovery command”; it is not an actual visible failure capture.
- Missing AI executable: supplied artifact does not name the selected executable and provides no installation or configuration command.

LARGEST_GAP: `/fixture/project` is absent, blocking the first required session from being created.

NEXT_FIX: Provision `/fixture/project` in the container fixture before running the walkthrough.

SCREEN_VERDICT: first-run 160x48 PASS
SCREEN_VERDICT: first-run 100x30 PASS
SCREEN_VERDICT: main-list 160x48 REJECT
SCREEN_VERDICT: main-list 100x30 PASS
SCREEN_VERDICT: new-session 160x48 REJECT
SCREEN_VERDICT: new-session 100x30 PASS
SCREEN_VERDICT: help 160x48 PASS
SCREEN_VERDICT: help 100x30 PASS
SCREEN_VERDICT: preview 160x48 REJECT
SCREEN_VERDICT: preview 100x30 REJECT