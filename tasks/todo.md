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

- [x] P2-3 root cause of the "8-minute Stop lag" from the conductor transcript + hook file (read-only); write it up.
- [x] P1-1 C: banner boundary = a LATER submitted prompt (new turn), not the failed turn's own summary line; failed-last-turn + recovered tails as tests; auth hold arms on a mid-turn 401.
- [x] P1-2 A: codex banner cleared by a later `›` turn / `•` reply / live busy tail; tests.
- [x] P2-5 remove the running-fast-path CapturePane; completed-turn verdict rides the captures GetStatus/GetSubstate already make; prove 0 tmux calls.
- [x] P2-4 persist hook-lag samples on the instance record (tool_data.hook_lag) so one-pass CLI callers, the daemon and the TUI agree; CLI-path test.
- [x] P2-6 daemon test: lag flip then late Stop = one transition + one [DONE].
- [x] P2-7 codex auth patterns narrowed to the observed banners; a warning mentioning authentication never becomes auth-401.
- [x] P2-8 substate_detail parity: session status --json, status_stale, web MenuSession, remote JSON; golden/shape tests; docs.
- [x] go build/vet; Docker suites; live read-only check; code-simplifier; commit; RESULTS.md + PR-BODY.md.

## Round 3 (rereview-status-lights-r2: HOLD, 2 P1 + 2 P2 + 2 P3)

- [x] P1-1 purge `out/agent-deck` from history (soft reset + recommit, `out/` in .gitignore); verify no `out/` object in rc..HEAD.
- [x] P1-2 retract the ScheduleWakeup "no Stop hook" root cause everywhere; cause = unknown; add per-instance hook event history (`<id>.events.jsonl`, last 200, health kill switch) + tests.
- [x] P2-3 same-pass busy capture reverts a record-driven `waiting` to `running`; CLI-path test.
- [x] P2-4 `session children --json` / `--follow` take the one `Substate()` capture per child; fix the "accumulates" claim.
- [x] P3-5 `ReadAllStatuses` guards `json_extract` with `json_valid`; test with `''` and `'not json'`.
- [x] P3-6 codex banner scan ignores `■` inside tool output (indented / under `└`); `cat` output test.
- [x] go build/vet; Docker suites; live read-only check; code-simplifier; commit; RESULTS.md + PR-BODY.md.

## Round 4 (rereview-status-lights-r3: PASS, 1 P2 + 1 P3 before merge)

- [x] P2 hook-context children summary takes no pane capture (`buildChildRows(kids, cachedChildStatus)`); `session children --json`/`--follow` keep sampling; 0-tmux-call test; disclosure in hook_lag.go, CHANGELOG, PR-BODY.
- [x] P3 hook-lag samples ordered at millisecond resolution (`last_sample_at_ms`, round-3 seconds field still read); a busy sample always clears the run; same-second idle-then-busy test.
- [x] go build/vet; Docker session + cmd suites; live read-only check; commit; RESULTS.md + PR-BODY.md.
