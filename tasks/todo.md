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
# fix/messaging-20260918 — messaging spine audit fixes (P1-1..P3-1)

- [x] P1-1 hooks install absolute binary path; `hooks status` PATH shadow + version mismatch; idempotent rewrite of bare entries
- [x] P1-2 ValidateTranscriptPath accepts CLAUDE_CONFIG_DIR + account slot dirs + worker-scratch, EvalSymlinks both sides, fail closed
- [x] P1-3 drain + sweeps take the inbox flock (lock order: inbox flock → consumedTurnsMu → inboxWriteMu)
- [x] P1-4 drop conductor-liveness gate; child_removed log-only; `inbox drain` exit 4 only with --strict, print per-store counts
- [x] P2-1 sync install exports AGENTDECK_STOP_SYNC marker; async-installed hook never drains
- [x] P2-2 per-target send lock in performSend (tmux + socket), bounded wait, "target busy with another send" verdict
- [x] P3-1 _unowned excluded from TTL sweep (sweep locked via P1-3)
- [x] go build/vet on host; Docker suite; code-simplifier; live `hooks status` read-only proof
- [x] RESULTS.md, PR-BODY.md (sanitized), commits with Claude-Session trailer

## Round 2 (review HOLD 7a5ae428)

- [x] P1-A stable hook path (invoked symlink, never a Cellar dir) + self-heal on daemon start and `hooks status` (dangling / version-mismatched entry rewritten, idempotent, atomic, logged once); tests: Cellar-style upgrade heals, stable symlink chosen
- [x] P1-B marker-less Stop entry keeps draining; only an explicit non-sync marker disables; unpinned check requires the marker so the heal / TUI path adds it; tests
- [x] P2-C transcript roots include every [groups.*.claude] and [conductors.*.claude] config_dir; conductor fixture test
- [x] P2-D wake-nudge idle gate re-probes status (hook fast path, pane fallback) under the daemon's probe budget; test
- [x] P3 nudge subprocess timeout covers the send lock wait (constants tied); test
- [x] go build/vet host; Docker suite; code-simplifier; live `hooks status` read-only (on a copy of settings.json)
- [x] RESULTS.md, PR-BODY.md, commits with Claude-Session trailer

## Round 3 (review HOLD b18124b1)

- [x] F1 lossless heal: order-preserving JSON round trip, only agent-deck entries touched, atomic write, one-time `settings.json.bak-agentdeck-<ts>`, no write when unchanged, malformed JSON → error, no write; tests (user hooks + timeout + prompt hooks + custom matchers survive; idempotent; malformed)
- [x] F2 pin only stable install paths (/opt/homebrew/bin, /usr/local/bin, ~/.local/bin, ...); dev build → "unpinnable dev build: hooks keep the bare command", never written; `hooks status` read-only; older binary never heals a newer pin; tests per path class
- [x] F3 handler enforces "async install never drains" at drain time (settings.json Stop entry form + marker); test
- [x] F4 CLI-installed (drifted but present) entries count as installed for the TUI prompt; drift repaired silently, no prompt; test
- [x] go build/vet host; Docker suite; code-simplifier; live `hooks status` mtime proof
- [x] RESULTS.md, PR-BODY.md (sanitized), commits with Claude-Session trailer
