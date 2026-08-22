VERDICT: REJECT

REASON: The evidence does not prove a completed onboarding journey. `/fixture/project` is absent, first-run stops at the Claude Hooks prompt rather than a useful empty state, attach/detach is not demonstrated, and the injected missing-executable cases contain no application recovery output.

SEVERITY: BLOCKER

STEPS:

1. Ran `agent-deck`. Welcome wizard appeared. Elapsed: 2 seconds.
2. Pressed `Enter` through first-run setup. Flow reached “Claude Code Hooks — Install / Skip,” but no completed empty state was captured. Elapsed: 6 seconds.
3. Checked `/fixture/project`; it does not exist. A shell session there could not be created as requested. Elapsed: 7 seconds.
4. Pressed `?`. Help opened and clearly showed `Enter` to attach and `Ctrl+Q` to detach. Elapsed: 9 seconds.
5. Pressed `n`. New Session opened with shell and AI tools including Claude, Gemini, OpenCode, Codex, and others. `Ctrl+S` was shown as the create key. Elapsed: 11 seconds.
6. Attach/detach was not exercised successfully; supplied captures only show errored seeded sessions. Elapsed: not demonstrated.
7. Missing-tmux-session preview showed `No tmux session running`, `R Restart`, and `Enter - attach (will auto-start)`. Elapsed: visible in capture.
8. Missing-tmux-executable artifact only said “Injected scenario: tmux executable absent.” It did not show an Agent Deck error naming `tmux` or a concrete next command. Elapsed: not demonstrated.
9. Missing-tool artifact only said “Injected scenario: selected AI executable absent.” It did not identify the selected executable or provide an install/alternative command. Elapsed: not demonstrated.

DOC_DRIFT:

- README: `agent-deck                        # Launch TUI`  
  Observed: launches the first-run wizard; it does not immediately reach the TUI list.
- README: ``Enter` | Attach to session`  
  Observed: Help agrees, but the actual attach journey was not demonstrated.
- README: ``n` | New session`  
  Observed: matches.
- README: ``?` | Full help`  
  Observed: matches.
- README Quick Start contains no detach instruction. Observed help says `Ctrl+Q Detach from session`.
- README does not describe the first-run wizard or Claude Hooks prompt.
- README command `agent-deck add . -c claude` offers no missing-Claude recovery guidance.

RECOVERY_GAPS:

- Missing `tmux` executable: no captured application error, dependency name, or concrete next command such as `sudo apt install tmux`/`brew install tmux`.
- Missing AI executable: no captured application error identifying the selected tool and no install command or “choose shell/another installed tool” action.
- Missing tmux session: recovery is actionable, but wording conflicts between `R Restart` and `Enter … auto-start`.
- `/fixture/project` is missing, preventing the required shell-session happy path.

LARGEST_GAP: The required project directory is absent, blocking session creation before attach/detach can be validated.

NEXT_FIX: Ensure `/fixture/project` exists in the fixture before the walkthrough begins.

SCREEN_VERDICT: error-preview 160x48 PASS  
SCREEN_VERDICT: error-preview 100x30 PASS  
SCREEN_VERDICT: missing-tmux-session 160x48 REJECT  
SCREEN_VERDICT: missing-tmux-session 100x30 REJECT  
SCREEN_VERDICT: missing-tmux-executable 160x48 REJECT  
SCREEN_VERDICT: missing-tmux-executable 100x30 REJECT  
SCREEN_VERDICT: missing-tool 160x48 REJECT  
SCREEN_VERDICT: missing-tool 100x30 REJECT