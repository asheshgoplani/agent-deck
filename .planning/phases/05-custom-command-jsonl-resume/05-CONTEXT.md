# Phase 5: Custom-command JSONL resume (REQ-7 fix) - Context

**Gathered:** 2026-04-15
**Status:** Ready for planning
**Source:** User-supplied direct context from conductor orchestrator (2026-04-15 incident) + `docs/SESSION-PERSISTENCE-SPEC.md` REQ-7 (spec updated same day) + live code inspection.

<domain>
## Phase Boundary

Phase 5 closes spec REQ-7 and REQUIREMENTS entries PERSIST-11, PERSIST-12, PERSIST-13, TEST-09. It is the **structural code-layer** fix that retires the ops-layer `~/.agent-deck/conductor/agent-deck/start-conductor.sh` workaround landed 2026-04-15 (the wrapper auto-picks the latest JSONL and passes `--resume`). After Phase 5, no wrapper hack is ever needed: every Claude-compatible session with an empty `ClaudeSessionID` will discover and resume the latest JSONL transcript as a default behavior of `Instance.Start()`.

**In scope:**
- `internal/session/instance.go` — the two dispatch sites at `:1893` (`Start()`) and `:2019-2033` (`StartWithMessage`) where `IsClaudeCompatible && ClaudeSessionID == ""` currently hands to `buildClaudeCommand` (fresh-UUID path).
- `internal/session/claude.go` — home for a new pure discovery helper `discoverLatestClaudeJSONL(projectPath string) (uuid string, found bool)` that reuses the path-encoding + directory-read + UUID-regex logic already present in `findActiveSessionID` (`:332-399`) but WITHOUT the 5-minute recency cap.
- `internal/session/userconfig.go` — only if a new config knob is needed (almost certainly not; the discovery is silent and default-on, matching how Phase 3 resume already works).
- `internal/session/session_persistence_test.go` — append one new test `TestPersistence_CustomCommandResumesFromLatestJSONL` (test #9 in REQ-3's enumeration; spec line 81).

**Out of scope (scope creep must halt the plan and escalate):**
- Any change to `Restart()` or `buildClaudeResumeCommand` — those are Phase 3 surface and must not be touched. The helper call site is exclusively on the Start-path BEFORE the existing resume/fresh gate.
- Any change to `fork` semantics, hook sidecar handling, or MCP attach flow.
- Any change to Codex / Gemini / OpenCode resume (spec REQ-7 non-goal: "Not scanning for resume across `tool: codex` or `tool: gemini`").
- Any modification to `start-conductor.sh` or conductor ops files — that's an operator-owned artifact now superseded by this phase.
- Any push/tag/PR/gh release/gh pr create/merge — hard repo rule per ROADMAP.md and CLAUDE.md.

</domain>

<decisions>
## Implementation Decisions

### D-01. New helper is pure (no `Instance` mutation)
- **Locked:** `discoverLatestClaudeJSONL(projectPath string) (uuid string, found bool)` lives in `internal/session/claude.go`, takes a project path, returns the UUID of the newest UUID-named JSONL by `mtime` (or `"", false`). It does NOT accept or mutate an `Instance`.
- **Why:** Purity makes the unit test trivial (stage JSONLs in a temp dir, assert return value), avoids coupling the discovery surface to Instance lifecycle, and mirrors the existing `findActiveSessionID` signature style. `Instance.Start()` owns the write-through.
- **Non-negotiable.**

### D-02. Write-through persistence happens in `Instance.Start()` BEFORE spawn
- **Locked:** On `found == true`, `Start()` sets `i.ClaudeSessionID = uuid`, calls the instance-storage Save method (same one used elsewhere in this file), then invokes `i.buildClaudeResumeCommand()` as the EXISTING `ClaudeSessionID != ""` branch would have. The dispatch at `instance.go:1893` becomes: `if i.ClaudeSessionID == "" { if uuid, found := discoverLatestClaudeJSONL(i.ProjectPath); found { i.ClaudeSessionID = uuid; _ = i.save(); } }` placed as a prelude; the existing `if i.ClaudeSessionID != ""` branch below then fires naturally.
- **Why:** Persistence before spawn means the next restart — even after a crash between discovery and a successful resume — sees a populated `ClaudeSessionID` and goes down the fast Phase 3 path, never re-scanning. This is the write-through cache pattern identical to how Phase 3 handles the hook-sidecar binding.
- The exact save method name will be confirmed by the planner when reading `instance.go` (candidates: `i.save()`, storage-layer call, or via the storage manager that already runs on every `stopped`/`error` transition).

### D-03. Same helper call from `StartWithMessage`'s claude branch at `:2019-2033`
- **Locked:** `StartWithMessage` has the same `if i.ClaudeSessionID != "" { … buildClaudeResumeCommand() } else { buildClaudeCommand(…) }` structure. The same prelude goes there — do NOT duplicate the logic, wrap it in a tiny helper `i.ensureClaudeSessionIDFromDisk()` or inline it identically at both sites (planner's call; either is acceptable).
- **Why:** Spec PERSIST-11 says "Any code path that starts a Claude session ... MUST resolve". Missing `StartWithMessage` would leave the `agent-deck session send --initial-message` path broken for custom wrappers.

### D-04. No `Command`-field branching
- **Locked:** Discovery runs on every Claude-compatible start with empty `ClaudeSessionID`, regardless of whether `i.Command` is empty (default wrapper) or a path to a custom script. Spec REQ-7 acceptance line: "No code path branches on 'custom command ⇒ skip resume'."
- **Why:** The 2026-04-15 incident surfaced specifically because custom wrappers went fresh; eliminating the branch is the whole point.

### D-05. Newest JSONL by `ModTime()`, no recency cap
- **Locked:** Discovery walks `<configDir>/projects/<encoded>/`, filters to `uuidSessionFileRegex.MatchString(basename)` (regex already in `claude.go:20`), skips any `agent-*` prefixed files, picks max by `info.ModTime()`. No 5-minute filter (that was appropriate for `findActiveSessionID` which detects a running session, not for selecting a resume target at start-up).
- **Why:** Spec: "If multiple JSONLs exist in the project dir, the most recently modified one is chosen." A five-minute cap would make every cold-boot resume fail.

### D-06. Use the existing `ConvertToClaudeDirName` + `EvalSymlinks` conventions
- **Locked:** Path encoding is the same as `sessionHasConversationData` (`instance.go:4854-4863`) and `findActiveSessionID` (`claude.go:343`). `filepath.EvalSymlinks` first, then `ConvertToClaudeDirName`, then join under `GetClaudeConfigDir() + "/projects/"` (falling back to `$HOME/.claude`).
- **Why:** Three implementations already encode the path this way; forking a fourth would introduce a macOS `/tmp` vs `/private/tmp` drift and fail silently on mount paths. The spec's example path (`-home-u--agent-deck-conductor-agent-deck`) is already what `ConvertToClaudeDirName` produces for `/home/u/.agent-deck/conductor/agent-deck`.

### D-07. New log line for the discovery path
- **Locked:** When `found == true` and we populate `ClaudeSessionID`, emit ONE `sessionLog.Info` line: `resume: id=<uuid> reason=jsonl_discovery` (slog attrs: `instance_id`, `claude_session_id`, `path`, `reason=jsonl_discovery`). This fires BEFORE the existing `buildClaudeResumeCommand` which will in turn emit its own `resume: id=… reason=conversation_data_present|session_id_flag_no_jsonl`. Two lines in the log for Phase 5 starts is acceptable and documented — they're distinguishable by `reason=`.
- **Why:** Observability parity with Phase 3's OBS-02 contract; lets `grep 'resume:' ~/.agent-deck/logs/*.log` show the discovery happened. No new requirement added — it's an internal consistency choice flagged in the Phase 5 ROADMAP success criteria (item 8).

### D-08. TDD ordering (RED → GREEN → REFACTOR) as separate commits
- **Locked:** Plan 05-01 lands TEST-09 in RED state (test compiles, runs, fails against unmodified `Start()` with a clear "`--resume` expected, got `--session-id` or fresh" message). Plan 05-02 adds `discoverLatestClaudeJSONL` + wires it into `Start()` and `StartWithMessage` + persistence — turns TEST-09 GREEN without touching any Phase 3 behavior. Plan 05-03 (if needed per planner's judgment) refactors: consolidates the two call sites via a small `ensureClaudeSessionIDFromDisk` method and adds the `resume: … reason=jsonl_discovery` log line. Each plan is one atomic commit with the `Committed by Ashesh Goplani` trailer.
- **Why:** Repo CLAUDE.md "Hard rules": `TDD always — the regression test for a bug lands BEFORE the fix.` Also mirrors Phases 1→2, 1→3 test-then-fix cadence.

### Claude's Discretion
- Whether 05-02 and 05-03 collapse into a single plan (TDD GREEN that already writes the log line cleanly, no REFACTOR needed) or split for reviewability — planner decides based on diff size target.
- Exact test-helper reuse from the Phase 1 `session_persistence_test.go` file — whether to add a new `mkTempProjectWithJSONLs(t, n int)` helper or inline `os.WriteFile` calls in TEST-09. Planner's call; either meets the spec.
- Whether `ensureClaudeSessionIDFromDisk` returns an error or is void (it logs and proceeds, mirroring how `findActiveSessionID` swallows directory-read errors). Planner decides; spec PERSIST-13 says "no error is raised" so void-with-silent-skip is the default.

</decisions>

<canonical_refs>
## Canonical References

**Downstream agents MUST read these before planning or implementing.**

### Specification (single source of truth)
- `docs/SESSION-PERSISTENCE-SPEC.md` — REQ-7 at lines 116-137; test enumeration at line 81; non-goals at 135-137. The 2026-04-15 incident narrative (why REQ-7 exists) is embedded in lines 123-124.
- `docs/session-id-lifecycle.md` — invariants for session-ID binding; Phase 5 discovery is read-only disk scan + write-through to instance storage and MUST NOT violate the "no disk-scan authoritative binding" rule (PERSIST-10). Interpretation: disk scan is a bootstrap input to instance storage, storage remains authoritative on all subsequent reads.

### Live code landmarks (planner's `read_first` MUST include these)
- `internal/session/instance.go:1873-1919` — `Instance.Start()`; the Claude-compatible branch at `:1882-1901` is the primary dispatch site for Phase 5's new prelude.
- `internal/session/instance.go:2019-2033` — `StartWithMessage()` Claude-compatible branch; the second dispatch site.
- `internal/session/instance.go:4152-4234` — `buildClaudeResumeCommand()`; Phase 5 reuses this unchanged once `ClaudeSessionID` is populated.
- `internal/session/instance.go:4845-4926` — `sessionHasConversationData()`; the existing path-encoding + file-existence pattern Phase 5 mirrors for directory listing.
- `internal/session/claude.go:15-27` — `claudeDirNameRegex`, `uuidSessionFileRegex`, `ConvertToClaudeDirName`. Reuse verbatim.
- `internal/session/claude.go:332-399` — `findActiveSessionID()`; existing "latest JSONL by mtime" logic. Phase 5's new helper extracts the shared kernel and drops the 5-minute cap; do NOT just edit `findActiveSessionID` in place (it's used by session-ID reconciliation on `:2602-2660` and has a semantic reason for the recency filter there).
- `internal/session/gemini.go:30-54` — `HashProjectPath`. Not used for Claude JSONL paths but referenced here so the planner knows Gemini has its own convention and does NOT conflate the two.

### Phase 3 resume contract
- `.planning/phases/03-resume-on-start-and-error-recovery-req-2-fix/03-03-PLAN.md` — the plan that originally routed `Start()`/`StartWithMessage()` through `buildClaudeResumeCommand`. Phase 5 extends that contract; it does not replace it.
- `internal/session/session_persistence_test.go` — contains TEST-01..TEST-08. TEST-09 appends here; reuse any fixture helpers already defined in that file.

### Hard gates (from user + repo CLAUDE.md)
- Repo `CLAUDE.md` "Session persistence: mandatory test coverage" section — `internal/session/instance.go`, `internal/session/userconfig.go`, `internal/tmux/**`, `cmd/session_cmd.go`, `cmd/start_cmd.go`, `cmd/restart_cmd.go`, `internal/session/storage*.go`, `scripts/verify-session-persistence.sh` and `CLAUDE.md` itself are under the mandate. Phase 5 touches `instance.go` and creates a test, so the eight existing tests + TEST-09 MUST all pass in the final commit; the PR description MUST include `go test -run TestPersistence_ ./internal/session/... -race -count=1` output.
- `bash scripts/verify-session-persistence.sh` MUST still exit 0 on the conductor host after Phase 5 lands — do not regress it.
- No `git push`, no `git tag`, no `gh release`, no `gh pr create`, no `gh pr merge`. No Claude attribution in commit messages — sign as `Committed by Ashesh Goplani` only.
- No `rm`; use `trash`.
- Use `-p personal` profile for any manual verification inside agent-deck on the conductor.

</canonical_refs>

<specifics>
## Specific Ideas

- Test fixture for TEST-09: create two temp JSONLs with `os.Chtimes` setting mtimes 10s apart, assert newer wins. Use `t.TempDir()` for isolation; clean up is automatic.
- JSONL content minimum: the file need not contain valid JSON for discovery — the helper only reads the filename. (`sessionHasConversationData` checks content; `discoverLatestClaudeJSONL` does not.)
- Conductor reproduction path: `ProjectPath` on the conductor instance is `~/.agent-deck/conductor/agent-deck`; `ConvertToClaudeDirName` produces `-home-<user>--agent-deck-conductor-agent-deck` which matches the on-disk directory the user confirmed has ~10 JSONLs.
- Persistence test: after calling `Start()` (or the helper that it uses), reload the instance from storage (JSON file under `~/.agent-deck/<profile>/instances/<id>.json` or whatever the storage manager uses) and assert `ClaudeSessionID` is populated. This is what PERSIST-12 requires and is the most likely Dimension-8 failure if the planner forgets to call save.

</specifics>

<deferred>
## Deferred Ideas

- **Codex and Gemini parity** — spec REQ-7 non-goal 137. Tracked in backlog for post-v1.5.2 work; separate transcript formats require per-tool discovery helpers.
- **UI-01 `↻` glyph** — still P2 from the v2 requirements block; not lit up by Phase 5 directly but its implementation would be easier now that disk-discovery populates `ClaudeSessionID`. Deferred to v2.
- **Removing the `start-conductor.sh` wrapper** — spec non-goal 136. The wrapper is harmless after Phase 5 (it'll just find the same JSONL first and pass `--resume`, which agent-deck would now do anyway). Leaving it in place keeps operators' ~/.agent-deck/conductor/ untouched.

</deferred>

---

*Phase: 05-custom-command-jsonl-resume*
*Context gathered: 2026-04-15 from user-supplied conductor context + spec REQ-7 + live code inspection at instance.go:1893/2033 and claude.go:332.*
