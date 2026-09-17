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
