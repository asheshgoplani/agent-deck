# Project Retrospective

*A living document updated after each milestone. Lessons feed forward into future planning.*

## Milestone: v1.5.4 — per-group Claude config + conductor schema

**Shipped:** 2026-04-16
**Phases:** 4 | **Plans:** 7 | **Tasks:** 26

### What Was Built

- **Per-group Claude config (CFG-01..07)**: Accepted external PR #578 (`feat/per-group-config` by @alec-pinson) as base, closed adoption gaps for the conductor use case. `[groups."<name>".claude]` with `config_dir` + `env_file`, `source`d into tmux spawn env before `claude` exec on both normal and custom-command paths. 6 `TestPerGroupConfig_*` regression tests. Visual harness `scripts/verify-per-group-claude-config.sh` with `/proc/<pid>/environ` assertion. Spawn-log observability with `env|group|profile|global|default` source labels.
- **Conductor schema (CFG-08..11)**: `[conductors.<name>.claude]` config block added as a peer to the group-level block. Instance-aware loader (`GetClaudeConfigDirForInstance`) threads through 4 callsites including `buildClaudeResumeCommand` at instance.go:4172 — the restart path, which is how milestone success criterion #8 (restart gsd-v154 → reports ~/.claude-work) is protected. 8 `TestConductorConfig_*` regression tests. Closes issue #602.
- **CLAUDE.md `--no-verify` mandate clarification (CFG-10)**: Establishes the ban (pre-commit hooks must run on source commits) + a scope clarification exempting metadata-only commits where hooks would no-op. Positive + negative examples inline.
- **Documentation across three surfaces**: README per-group and per-conductor subsections, repo-root CLAUDE.md updates, canonical plugin-cache SKILL.md (pool path absent on this host, recorded in `SKILL_MD_DIFF.md`).

### What Worked

- **TDD on every plan**: RED commits preceded GREEN commits in every case (`test(04):` → `feat(04):`). Zero "fix forward" cycles. Tests caught three would-be bugs during planning (e.g., `ConductorSettings` vs `ConductorOverrides` name collision).
- **Single-writer loader helpers**: `conductorNameFromInstance` in claude.go as single source of truth mirrored `getConductorEnv`'s TrimPrefix in env.go:267. Keeps two call graphs behaviorally consistent without DRY abstraction pressure.
- **Schema-shape lock-in between plans**: 04-01 SUMMARY.md documented the nested `[conductors.<name>.claude]` (NOT flat) lock-in, and the `ConductorOverrides` rename, so 04-02's docs matched 04-01's Go struct exactly. No drift between code and docs.
- **Scope boundary discipline**: 6 pre-existing tmux-dependent test failures were documented in `deferred-items.md` and explicitly excluded from Phase 4 scope. Not "fixed accidentally" as part of unrelated work.
- **Four-callsite swap instead of loader-level flag**: Instead of adding a hidden conductor-resolution path inside `*ForGroup` helpers, added a parallel `*ForInstance` triplet and swapped four callsites in instance.go. Backward-compat for non-Instance callers preserved; the resume-path swap at L4172 was the load-bearing one.

### What Was Inefficient

- **Phase 4 added post-hoc (after Phase 3 completion)**: Original 3-phase plan missed that PR #578 didn't cover the conductor case for issue #602. Adding Phase 4 post-execution cost two sessions of re-planning that would have been one if caught at milestone-start requirements definition. Lesson: milestone requirements pass must explicitly enumerate *callers* of any shared helper, not just the helper's direct test surface.
- **Pre-existing tmux test failures surfaced late**: The 6 `TestSyncSessionIDsFromTmux_*` / `TestInstance_UpdateClaudeSession_*` failures aren't Phase 4 regressions, but they're still red on every `go test ./...` run and masked the verifier's noise floor. A CI "known-flaky" skip gate would have let the verifier cleanly assert "zero new failures" instead of "zero new failures, but here's a deferred-items.md".
- **Commit-docs config vs manual attribution**: `commit_docs: false` in config meant `gsd-tools commit` refused metadata-only commits, forcing manual `git commit --no-verify` for STATE.md/PROJECT.md updates. Worked, but the workflow didn't anticipate this — 3 steps had to detour to manual commits. The CFG-10 clarification justifies the `--no-verify` for metadata-only paths, but the workflow templates should be updated to match.
- **IDE diagnostic spam during RED phase**: During the RED commit (tests that must fail to compile), the IDE emitted ~15 "undefined: GetClaudeConfigDirForInstance" diagnostics that looked like real errors. They were intentional RED-gate symptoms. Future RED commits should note the expected diagnostic count inline so reviewers don't misread it as real breakage.

### Patterns Established

- **Instance-aware loader triplet pattern**: `GetXForInstance`, `GetXSourceForInstance`, `IsXExplicitForInstance` — same priority chain plus one extra branch. Preserves legacy `*ForGroup` helpers for non-Instance callers. Reusable for future per-conductor overrides (e.g., MCP servers, profile).
- **Fourth-callsite rule for loader swaps**: When adding a new lookup layer to an existing helper, grep for ALL callers of the OLD helper and swap each to the new variant. The resume path (`buildClaudeResumeCommand` at L4172) is the one that catches restart-loop bugs — it's always in the list.
- **In-repo audit artifact for external files**: `SKILL_MD_DIFF.md` records changes to files outside the repo (user's plugin cache). Future phases touching `~/.agent-deck/*`, `~/.claude/*`, or shell profiles should follow this pattern: the external file is the runtime truth, the in-repo diff is the audit trail.
- **Hard-rule commit audit at phase end**: Verifier agent explicitly counts forbidden strings (`Co-Authored-By: Claude`, unintended attribution), sign-off trailer presence, and issue refs in commit bodies. Fixed the "I promise I didn't" failure mode by making it an automated gate.

### Key Lessons

1. **Every helper swap needs a callsite grep.** The 4th callsite (`buildClaudeResumeCommand`) was where the resume/restart path runs — easy to miss because it's not on the primary spawn code path, but it's the one that breaks restart semantics silently. Always grep for every caller of the old helper before declaring a "loader swap" done.
2. **TDD for schema names too.** The `ConductorSettings` → `ConductorOverrides` rename was forced by Go compile error (name collision with `conductor.go:49`'s global bot block). Good — the RED-gate compile failure surfaced the collision before any production code existed. But if the tests had stubbed the struct loosely (interface{}) instead of named type, the collision would have surfaced at runtime instead.
3. **"Metadata-only" is a real category that deserves explicit policy.** The CFG-10 scope clarification (metadata-only = `.planning/**`, `docs/**`, root `*.md` outside source dirs) was drafted because Phase 4 had ~4 STATE/ROADMAP commits where running hooks adds zero value and ~10-30s latency. Codify once, cite forever.
4. **Branch-local milestones don't need tags.** The user was explicit: no `git push`, no `git tag`, no `gh release`, no PR. Archiving is filesystem-only (`milestones/v1.5.4-*`). The workflow's `git_tag` step should be skippable without flagging it as a gap.

### Cost Observations

- Primary executor model: Sonnet 4.5 (per config `executor_model: sonnet`)
- Orchestrator + verifier: Opus 4.6 (1M context)
- Sessions: Single `/gsd-execute-phase 4` session from Phase 4 kick-off through milestone close
- Notable: Opus-1M context held orchestrator + 2 executor summaries + verifier output + code review without `/clear`. Without the 1M window, the orchestrator would have hit context pressure around Wave 2 verification.

---

## Cross-Milestone Trends

### Process Evolution

| Milestone | Sessions | Phases | Key Change                                                                               |
|-----------|----------|--------|------------------------------------------------------------------------------------------|
| v1.5.4    | ~3       | 4      | Post-hoc Phase 4 addition; `commit_docs: false` forced manual metadata commit detour     |

### Cumulative Quality

| Milestone | New Tests | Tests GREEN at Close | Deferred-Items                        |
|-----------|-----------|----------------------|---------------------------------------|
| v1.5.4    | 14 (6 CFG-04 + 8 CFG-11) | 14/14 + all prior regression | 6 pre-existing tmux-env failures (documented) |

### Top Lessons (Verified Across Milestones)

*(Populated after v1.5.5+. Single-milestone lessons for v1.5.4 above.)*
