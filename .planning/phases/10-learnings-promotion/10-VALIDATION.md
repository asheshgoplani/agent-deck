---
phase: 10
slug: learnings-promotion
status: draft
nyquist_compliant: false
wave_0_complete: false
created: 2026-03-07
---

# Phase 10 — Validation Strategy

> Per-phase validation contract for feedback sampling during execution.

---

## Test Infrastructure

| Property | Value |
|----------|-------|
| **Framework** | Manual verification (documentation phase, no code) |
| **Config file** | N/A |
| **Quick run command** | N/A |
| **Full suite command** | N/A |
| **Estimated runtime** | N/A |

---

## Sampling Rate

- **After every task commit:** Visual diff review of changed files
- **After every plan wave:** Read destination files and confirm content integrated correctly
- **Before `/gsd:verify-work`:** Read all 3 destination files and all 7 source files to confirm completeness
- **Max feedback latency:** N/A (manual verification)

---

## Per-Task Verification Map

| Task ID | Plan | Wave | Requirement | Test Type | Automated Command | File Exists | Status |
|---------|------|------|-------------|-----------|-------------------|-------------|--------|
| 10-01-01 | 01 | 1 | LEARN-01 | manual-only | `grep -c "event-driven\|parent linkage\|Enter key" ~/.agent-deck/conductor/CLAUDE.md` | N/A | ⬜ pending |
| 10-01-02 | 01 | 1 | LEARN-02 | manual-only | `grep -c "Claude-only\|map-codebase\|comprehensive specs" ~/.agent-deck/skills/pool/gsd-conductor/SKILL.md` | N/A | ⬜ pending |
| 10-01-03 | 01 | 1 | LEARN-03 | manual-only | `grep -c "Codex launch\|release sessions\|Gemini video" ~/.agent-deck/skills/pool/agent-deck-workflow/SKILL.md` | N/A | ⬜ pending |
| 10-02-01 | 02 | 2 | LEARN-04 | manual-only | `grep -c "promoted\|retired" ~/.agent-deck/conductor/*/LEARNINGS.md` | N/A | ⬜ pending |

*Status: ⬜ pending · ✅ green · ❌ red · ⚠️ flaky*

---

## Wave 0 Requirements

*Existing infrastructure covers all phase requirements.*

No test infrastructure needed for documentation curation.

---

## Manual-Only Verifications

| Behavior | Requirement | Why Manual | Test Instructions |
|----------|-------------|------------|-------------------|
| Universal patterns in CLAUDE.md | LEARN-01 | Documentation content quality requires human review | Read CLAUDE.md, confirm 10 patterns from research are present and well-integrated |
| GSD learnings in gsd-conductor SKILL.md | LEARN-02 | Content integration quality | Read SKILL.md, confirm 6 GSD learnings added to appropriate sections |
| Agent-deck ops in agent-deck skill | LEARN-03 | Content integration quality | Read SKILL.md, confirm 11 operational learnings added |
| LEARNINGS.md cleanup | LEARN-04 | Status tracking requires context | Read all 7 LEARNINGS.md files, confirm promoted entries marked, retired removed, duplicates consolidated |

---

## Validation Sign-Off

- [ ] All tasks have `<automated>` verify or Wave 0 dependencies
- [ ] Sampling continuity: no 3 consecutive tasks without automated verify
- [ ] Wave 0 covers all MISSING references
- [ ] No watch-mode flags
- [ ] Feedback latency acceptable
- [ ] `nyquist_compliant: true` set in frontmatter

**Approval:** pending
