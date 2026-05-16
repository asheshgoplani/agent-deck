# v1.9 Phase 1 — 12 Regression Test Picks

**Branch:** `feat/v1.9-phase1-tests` (off `origin/main` @ v1.8.3).
**Constraint:** test-only. No impl changes. If a test reveals a bug, file an issue.
**Selection rubric:** v1.8.x regressions (#864 #876 #856 #816 #881 #867) **or** P0 use-case scenarios from `USE-CASES-AND-TESTS.md` (S-LC-1, S-MS-3, S-FAIL-7, S-CLI-1, S-CLI-4) **or** parity gaps that #898 / #900 abstractions exposed but didn't cover.

| # | Case ID | Layer | Maps to | What it asserts |
|---|---|---|---|---|
| 1 | `prof-001` | `internal/session` | T2 / #881 / J3 | `GetEffectiveProfile` falls back to `config.json` `default_profile` when no env vars are set (priority 4 of the resolution ladder). Today only priorities 1–3 have tests — config-default is the fallback users hit on a clean machine, and a regression here would silently re-route every read to "default". |
| 2 | `prof-002` | `internal/session` | T2 / #881 / J3 | Full table-driven precedence chain: explicit > `AGENTDECK_PROFILE` > `CLAUDE_CONFIG_DIR` > config default > literal "default". Locks the contract documented in `config.go:301-336` so no re-ordering accident slips through. |
| 3 | `prof-003` | `internal/session` | T2 / #881 / TUI K2 | `profileFromClaudeConfigDir` directly: every documented mapping (`~/.claude-work` → "work", `~/.claude-personal` → "personal", `~/.claude` → "" no-inference, `/opt/claude-prod` → "prod") plus negative cases (empty, no dash, just trailing dash). Today this helper is only exercised through indirect paths in 2 tests. |
| 4 | `web-001` | `internal/web` | #867 / J2 (joeblubaugh) | `GET /api/menu` applies `refreshSnapshotHookStatuses` — fresh "waiting" hook lifts a stale "error" snapshot. Today only `GET /api/sessions` has this end-to-end assertion (`TestParity_WaitingStatusFlowsThroughHandler`); `handleMenu` calls the same helper at `handlers_menu.go:41` but is untested at the handler boundary. |
| 5 | `web-002` | `internal/web` | #867 / J2 | `GET /api/session/{id}` applies `refreshSnapshotHookStatuses` — same divergence-lift assertion for the per-session endpoint at `handlers_menu.go:72`. Without this, a refactor that drops the call site from one handler but not the other would slip. |
| 6 | `web-003` | `internal/web` | #867 defensive | `refreshSnapshotHookStatuses` tolerates nil snapshot, items with nil `Session`, items that are groups, and an empty loader return. The current implementation has guards but no test pins them — a future refactor that removes them would crash on real production snapshots that always interleave groups and sessions. |
| 7 | `send-001` | `cmd/agent-deck` | #876 / S-CLI-4 | `messageDeliveryToken` (the helper that gates body-in-pane delivery evidence at `session_cmd.go:2123`): empty/short → "", >64 chars → truncated to 64, whitespace-trimmed. Currently has zero direct tests. |
| 8 | `send-002` | `cmd/agent-deck` | #876 / S-CLI-4 (large-prompt receipt) | `sendWithRetryTarget(verifyDelivery=true)` accepts a 100KB-class message body match in the pane as positive delivery evidence — exercises `messageDeliveryToken`'s 64-char cap on the real call path. The existing `TestSendWithRetryTarget_VerifyDelivery_AcceptsMessageInPane` only tests a 19-char token; the silent-drop bug class manifests on multi-KB conductor prompts. |
| 9 | `rebind-001` | `internal/session` | #856 boundary | `clearRebindMtimeGrace` boundary: candidate.mtime - current.mtime ≥ 5s rebinds even if smaller; gap of 4s rejects. Existing `TestSessionIDFromHook_ClearRebindWinsOnMtimeGap` (instance_test.go:3576) covers the happy path with a generous gap; nobody pins the threshold itself, so a future "make it 30s" tweak would silently change behavior. |
| 10 | `status-001` | `internal/web` | J1 / S-CLI-1-SCHEMA-STABILITY | Every `session.Status` value serializes to its documented JSON literal through `GET /api/sessions`. The existing `TestParity_AllStatusesPreservedThroughGetSessions` only asserts round-trip; this asserts exact wire format (`"status":"running"`, etc.) so any future Status-marshal refactor that changes casing or symbol breaks the CLI/JS shape contract loudly. |
| 11 | `route-001` | `internal/web` | F8 / negative routing | `GET /api/session/{id}` for an unknown id returns 404 with `code:"NOT_FOUND"`, not 200/null or 500 — the per-session endpoint must be a hard negative under both no-overlay and overlay-present branches. Pins the contract that webMutations and overlay state never leak into the read-error path. |
| 12 | `status-stop-001` | `internal/web` | J5 / B14 / #867 negative | `GET /api/menu`: a "stopped" session is **never** flipped by a fresh "waiting" hook overlay. Asserts the user-intentional rule (Instance.UpdateStatus + applyHookStatusToMenuSession `StatusStopped` short-circuit) at the handler boundary so nobody wires hookStatusLoader past the stopped-stickiness guard. |

## Skipped on purpose

- **🚫 Untestable in CI**: WEB-K1/K2 (host-emulator paste/cmd-click), WEB-J4 multi-client tmux size negotiation, TUI G1 XTVERSION leak (host-PTY).
- **❓ Product decision pending**: WEB-A1–A10 topbar redesign, S-MS-4 sort-by-actionable, S-FAIL-13 multi-instance lock spec, mobile/tablet SLA.
- **Already covered**: #864 (`conductor/tests/test_python_compat.py`), #856 happy path (`TestSessionIDFromHook_ClearRebindWinsOnMtimeGap`), #876 verifyDelivery error (`TestSendWithRetryTarget_VerifyDelivery_ErrorsWhenNoEvidenceOfReceipt`).
- **Needs new infrastructure not in scope**: TUI lifecycle B1 (needs `fakeInotify`+`teatest`+`fakeClock`), hook event drop simulator (needs Linux real-fsnotify harness), CLI ↔ web cross-process fixture (needs separate test binary).

## Execution discipline

- One commit per case for clean review.
- After each commit: `go test -race -count=1` on the touched package.
- If a test fails on `main` (i.e. the case turns up a real bug), file an issue and add an `ACTION: PHASE1_FOUND_BUG` line to `RESULTS.md`. Do NOT fix the impl in this PR.
- `LEFTHOOK=0` only for pre-existing lint failures, documented per commit.
