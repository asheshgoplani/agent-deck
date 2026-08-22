VERDICT: REJECT
TIME_TO_FIRST_SESSION_SECONDS: 61
WRONG_ACTIONS: 1
LARGEST_GAP: Creating a session closes the form without confirmation, error, or a new session row.
EVIDENCE: first-run says “Agent Deck is a terminal session manager for AI coding agents” and asks me to press Enter to continue or Esc to use defaults; main-list shows `n/N New/Quick`; two documented create attempts returned to the unchanged three-session list; Enter did not attach; help reopened and `x` returned to the list.
REASON: No shell session was verifiably created or attached, so safe detach could not be completed; the one wrong action was launching inside tmux, which displayed an error and the next action `AGENT_DECK_ALLOW_OUTER_TMUX=1`.
SEVERITY: BLOCKER
SCREEN_VERDICT: first-run 160x48 PASS
SCREEN_VERDICT: first-run 100x30 PASS
SCREEN_VERDICT: main-list 160x48 REJECT
SCREEN_VERDICT: main-list 100x30 PASS
SCREEN_VERDICT: new-session 160x48 REJECT
SCREEN_VERDICT: new-session 100x30 REJECT
SCREEN_VERDICT: help 160x48 PASS
SCREEN_VERDICT: help 100x30 PASS
SCREEN_VERDICT: preview 160x48 REJECT
SCREEN_VERDICT: preview 100x30 REJECT
NEXT_FIX: After I submit New Session, keep the form open with a visible error or show a confirmation and select the newly created row.