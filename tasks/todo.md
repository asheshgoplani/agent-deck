# fix/status-lights-20260917 (status-light audit A–G)

- [x] Read audit RESULTS.md / ISSUES.md; capture the named panes read-only for test fixtures.
- [x] Failing-first tests from captured pane tails (tmux, session, ui golden).
- [x] F: same-line "… tokens" rule in hasClaudeBusyIndicator and hasClaudePrompt.
- [x] B: hook-lag rule (2 confirming samples) + substate hook-lag; contradictory running/idle pair closed.
- [x] A: codex "■" error banners (usage-limit with retry time, login) → error; pi documented.
- [x] C: newest signal wins — banner scans stop at a later completed turn.
- [x] D: feedback survey → interactive-menu (and waiting).
- [x] E: trust dialog → interactive-menu; codex picker → interactive-menu (explicit per-tool gate).
- [x] G: observation only, written up in RESULTS.md.
- [x] Host go build + go vet; Docker tmux/session/ui suites.
- [x] Read-only live check against profile personal; before/after table in RESULTS.md.
- [x] Commit with Claude-Session trailer; RESULTS.md + sanitized PR-BODY.md; nothing pushed.

## Round 2 (review-status-lights-r1: HOLD, 2 P1 + 6 P2)

- [ ] P2-3 root cause of the "8-minute Stop lag" from the conductor transcript + hook file (read-only); write it up.
- [ ] P1-1 C: banner boundary = a LATER submitted prompt (new turn), not the failed turn's own summary line; failed-last-turn + recovered tails as tests; auth hold arms on a mid-turn 401.
- [ ] P1-2 A: codex banner cleared by a later `›` turn / `•` reply / live busy tail; tests.
- [ ] P2-5 remove the running-fast-path CapturePane; completed-turn verdict rides the captures GetStatus/GetSubstate already make; prove 0 tmux calls.
- [ ] P2-4 persist hook-lag samples on the instance record (tool_data.hook_lag) so one-pass CLI callers, the daemon and the TUI agree; CLI-path test.
- [ ] P2-6 daemon test: lag flip then late Stop = one transition + one [DONE].
- [ ] P2-7 codex auth patterns narrowed to the observed banners; a warning mentioning authentication never becomes auth-401.
- [ ] P2-8 substate_detail parity: session status --json, status_stale, web MenuSession, remote JSON; golden/shape tests; docs.
- [ ] go build/vet; Docker suites; live read-only check; code-simplifier; commit; RESULTS.md + PR-BODY.md.
