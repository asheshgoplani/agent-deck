VERDICT: REJECT

REASON: The required happy path could not be completed. `/fixture/project` is absent and the read-only container prevents creating it or a fresh fixture HOME. Captures also omit the empty state, successful session creation, attachment, detachment, and both injected recovery outcomes.

SEVERITY: BLOCKER

STEPS:

1. Read README Quick Start: `agent-deck # Launch TUI`. Visible result: command matches installed binary location. Elapsed: 0s.
2. Checked fixture isolation. Visible result: fixture-owned HOME exists at `/tmp/agent-deck-gauntlet.MBYqvL`, but `/fixture/project` returns “No such file or directory.” Elapsed: 1s.
3. Attempted to create `/fixture/project`. Visible result: `mkdir: cannot create directory ‘/fixture’: Read-only file system`. Elapsed: 1s.
4. Reviewed first-run captures. Keys shown: `Enter` continues; `Esc` uses defaults. Visible result: welcome wizard explains tool and Claude configuration. Elapsed: 2s.
5. Reviewed post-launch captures. Visible result: no empty-state evidence; seeded groups and three errored shell sessions appear. At 160×48, a Claude hooks prompt obscures the expected main list. Elapsed: 2s.
6. Located session creation with `n`. Visible result: dialog offers `shell`, `claude`, `gemini`, `opencode`, `codex`, and other tools; `Ctrl+S` creates. Elapsed: 1s.
7. Located help with `?`. Visible result: help explicitly says `Enter Attach`, `Ctrl+Q Detach`, and `? Open this help`. Elapsed: 1s.
8. Tested missing-tmux PATH with only the Agent Deck binary available. Visible result: the TUI opened; selecting an errored session showed “No tmux session running” and suggested `R Restart` or `Enter`, but did not name a package-install command. Elapsed: 7s.
9. Missing-tool evidence only states that the scenario was injected. No resulting UI, dependency name, or recovery command was captured. Elapsed: 0s.
10. Total observed walkthrough time: approximately 15s, but the required successful workflow never completed.

DOC_DRIFT: README says `agent-deck add . -c claude # Add current dir with Claude`; the visible TUI’s new-session flow requires selecting a tool and using `Ctrl+S`, while the supplied walkthrough contains no successful execution proving the documented CLI command. README says `Enter | Attach to session`; on a selected group, the footer and behavior use Enter to toggle the group. The README does not describe the intervening first-run Claude hooks prompt.

RECOVERY_GAPS: Missing tmux: visible error is “No tmux session running” with `R Restart` and `Enter - attach (will auto-start)`; it does not state that the `tmux` executable is missing or provide a concrete installation/check command such as `tmux -V`. Missing AI tool: no failure screen was supplied, so there is no evidence that the executable is named or that a concrete next command is provided.

LARGEST_GAP: The required `/fixture/project` does not exist and cannot be created, blocking the earliest successful session outcome.

NEXT_FIX: Provide a writable, pre-created `/fixture/project` and writable isolated HOME/tmux fixture for the walkthrough.

SCREEN_VERDICT: first-run 160x48 PASS  
SCREEN_VERDICT: first-run 100x30 PASS  
SCREEN_VERDICT: main-list 160x48 REJECT  
SCREEN_VERDICT: main-list 100x30 REJECT  
SCREEN_VERDICT: new-session 160x48 REJECT  
SCREEN_VERDICT: new-session 100x30 REJECT  
SCREEN_VERDICT: help 160x48 PASS  
SCREEN_VERDICT: help 100x30 PASS  
SCREEN_VERDICT: preview 160x48 REJECT  
SCREEN_VERDICT: preview 100x30 REJECT