# Self-Improvement — full reference

A pipeline that lets an agent-deck conductor analyze its own past conversation transcripts to surface bugs, recurring patterns, capability discoveries, and improvement opportunities — then file the actionable ones as GitHub issues with privacy guards.

This document is the deep-dive. The [SKILL.md "Self-Improvement"](../SKILL.md) section has the quick-start.

## What it produces

Per conductor, in `~/.agent-deck/conductor/<name>/`:

| File | Purpose |
|---|---|
| `FINDINGS.md` | Raw synthesis from this run (regenerated each time) |
| `CAPABILITIES.md` | Curated inventory of what agent-deck does, mapped to surfaces (CLI/TUI/Web UI) with known issues |
| `analysis-manifest.json` | Tracking — sha256, line counts, analyzer session IDs per transcript |
| `analysis/` subdir | Working files: distilled transcripts, per-transcript reports, scripts, prompts |

## Architecture — 4 phases

```
   PHASE 0  Manifest build       Python, no LLM         discover transcripts
   PHASE 1  Distill              Python, no LLM         ~30× compression
   PHASE 2  Per-transcript       agent-deck sessions    1 LLM session per file
            analyzer                                    paced, resumable
   PHASE 3  Synthesize           agent-deck session     merge → FINDINGS.md
   PHASE 4  Curate / file        manual + scripts       FINDINGS → issues
```

All LLM work happens in spawned agent-deck sessions — never via direct Anthropic SDK. The orchestrator (`run-analyzers.sh`) drives sequential, paced spawning with `--wait` per session, manifest-based resume on failure, and 30s pacing between launches.

## Privacy pipeline — 3 layers before sharing

```
   FINDINGS.md (raw, conductor home, private)
       │
       ▼ Layer 1: regex sanitizer  (~50 ms, deterministic)
   FINDINGS.regex.md
       │
       ▼ Layer 2: AI sanitizer session  (~3 min, context-aware)
   FINDINGS.ai.md
       │
       ▼ Layer 3: independent AI auditor session  (~3 min, fresh eyes)
   FINDINGS.audit.md  →  verdict: SAFE_TO_SHARE | NEEDS_REVIEW | DO_NOT_SHARE
       │
       ▼ Layer 4: human review (mandatory, never skipped)
   FINDINGS.public.md  →  shareable
```

Layer 1 catches deterministic patterns (paths, tokens, IPs, UUIDs). Layer 2 catches contextual identifiers (people's names, internal project names, company-specific phrasing). Layer 3 is adversarial fresh-eyes review with no knowledge of what was redacted. Each layer covers the others' blind spots.

**Hard guarantees:**
- Filing flow refuses to submit if audit verdict isn't `SAFE_TO_SHARE`.
- Worker sessions are forbidden from running `gh issue create` directly.
- The `gh` command is printed in full before any prompt — user sees exactly what would happen.
- Manifest tracks every filed issue, dedup'ing future runs.

## When to run

| Trigger | Frequency |
|---|---|
| First run on a new conductor | once |
| User asks "self-improve" / "analyze yourself" | on demand |
| After agent-deck version bump | once per release |
| Weekly maintenance | optional |

The manifest's sha256 + line count tracking means re-runs are cheap: only new or grown transcripts get re-analyzed.

## Output schemas

### Per-transcript report (`analysis/reports/<sid>.md`)

```markdown
# Report: agent-deck / <session-id>

## Summary
## agent-deck capabilities exercised
## Issues encountered (with citations)
## User corrections / interventions
## Workflow patterns observed
## Capability discovery
## Honest assessment
```

### FINDINGS.md (synthesizer output)

```markdown
# FINDINGS — <conductor> — run <date>

## Capabilities exercised across all transcripts
## Recurring issues (≥2 reports)
## Single-occurrence issues worth investigating
## Recurring workflow patterns
## Capabilities discovered (not obviously documented)
## Per-report honest-assessment digest
## Headline summary
```

### CAPABILITIES.md (human-curated, durable)

```markdown
# agent-deck CAPABILITIES

| # | Capability | CLI | TUI | Web UI | Verified by |
|---|---|---|---|---|---|
| 1 | Manage sessions          | 🟡 | ⚪ | 🔴 | reports/... |
| ...                         |    |    |    |             |
```

Status legend: ✅ works, 🟡 partial, 🔴 buggy, ⚪ unknown, ➖ not-applicable.

## Lessons learned from real usage

Compiled from the first end-to-end run (May 2026, 7 conductors, 83 transcripts):

### What works well
- **Compression: ~30× consistently** across conductors with very different content (CLI work, business orchestration, research). The distiller's filter generalizes.
- **Sequential pacing avoids rate limits.** Two parallel batches (file-all + analyze-other) ran for 2+ hours without a single rate-limit failure at 30–60s pacing.
- **Manifest-based resume.** Mid-run failures cost nothing — re-running the script picks up where it left off via `report_path` existence checks.
- **Per-conductor isolation.** Each conductor's analysis lives in its own home; nothing crosses contexts. Cross-conductor synthesis happens at a separate, optional final step.
- **Cross-conductor validation in practice.** The Telegram poller storm bug appeared in two conductors (agent-deck, sherif) independently — that's strong "this affects real users, not one edge case" evidence.

### Footguns the analysis itself hit
- **`gh` label filter required** — AI-generated labels (`cli`, `telegram`, `conductors`) don't exist in the repo's label set; filing fails atomically until you filter. `file_issue.py` now strips unknown labels and keeps `bug` if present.
- **Auditor cosmetic vs blocking** — auditor's `SAFE_TO_SHARE` verdict is gating; minor cosmetic concerns are reported but don't block. Distinction matters.
- **Stale event replays** — agent-deck's transition-notifier issue (filed as a finding) actively repeats notifications for completed sessions during the run. Apply Behavior Rule #10 (change tactic after 3+ identical events).

### Things to add next iteration (not built yet)
- Cross-conductor meta-synthesizer (read all per-conductor FINDINGS.md → one MASTER-FINDINGS.md showing which bugs span multiple users)
- Throughput analytics: goal-latency, repeated-mistake-counter, idle-gap detection
- Issue dedup against existing GH issues using fuzzy title match (currently coarse 50% token overlap)
- Test-case sketch in analyzer output (so each filable bug comes with a draft regression test)

## Script reference

All scripts live in `scripts/self-improvement/` relative to this skill. Resolve paths via `$SKILL_DIR/scripts/self-improvement/` (see [SKILL.md Script Path Resolution](../SKILL.md)).

| Script | Purpose |
|---|---|
| `distill.py <input.jsonl> <output.md>` | Compress one JSONL transcript |
| `build_manifest.py` | Refresh `analysis-manifest.json` from disk state |
| `run-analyzers.sh` | Drive per-transcript analyzer batch (manifest-aware) |
| `analyze-all-conductors.sh` | Same as `run-analyzers.sh` but loops over multiple conductors |
| `list_candidates.py --findings <path>` | Parse FINDINGS.md → ranked candidates JSON |
| `sanitize.py <input> <output> [--map k=v]` | Regex layer of the privacy pipeline |
| `file_issue.py <body> --repo <r> --fingerprint <s>` | Wraps `gh issue create`, updates manifest |
| `file-issues.sh` | Interactive driver: pick → draft → audit → preview → file |

## Prompts

Five prompts under `scripts/self-improvement/prompts/`. Each is a worker-session contract — small, strict, single-purpose:

| Prompt | Used by | Output |
|---|---|---|
| `analyzer.md` | run-analyzers.sh per transcript | One report per the schema |
| `synthesizer.md` | run-analyzers.sh after all reports | One FINDINGS.md |
| `sanitizer.md` | file-issues.sh + standalone | Sanitized text + audit JSON |
| `auditor.md` | file-issues.sh + standalone | Independent audit report |
| `issue-drafter.md` | file-issues.sh per pick | Ready-to-file GH issue body |

Each prompt includes explicit privacy rules: do not include real names, paths, tokens, internal domains, etc. This is **Layer 0: prevention at source** — the cheapest privacy layer because no PII gets written in the first place.

## Cost estimate (typical run)

| Phase | Cost |
|---|---|
| Distillation (one conductor, ~10 transcripts) | ~$0, <1 min |
| Per-transcript analyzers (~10 transcripts) | ~$3, ~30 min |
| Synthesizer | ~$0.30, ~3 min |
| Per-issue draft+audit (during filing) | ~$1 per issue |
| End-to-end first run, one conductor | ~$5, ~35 min |
| Filing 20 issues from findings | ~$20, ~2 hours |

Subsequent runs on the same conductor are cheaper — manifest resume means only new/grown transcripts get analyzed.

## Related capabilities

- [Conductor setup](../SKILL.md) for setting up the conductor home this analyzes
- [`launch-subagent.sh`](../scripts/launch-subagent.sh) for the spawning primitive
- Per-conductor `state.json` / `LEARNINGS.md` / `task-log.md` survive Claude Code compaction and feed into the distiller's input
