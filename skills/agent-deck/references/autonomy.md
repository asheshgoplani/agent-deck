# Self-Improvement, Goals, and Trust-but-Verify

Transcript mining, goal-driven worker autonomy, and independent verification of completion claims.

## Self-Improvement

**Use when:** user says "self-improve", "analyze my conductor", "what bugs are we hitting", "file issues from my usage", or asks the conductor to learn from past conversations.

A pipeline that analyzes a conductor's own Claude Code conversation transcripts and surfaces actionable signal:

- **Bugs** — tool errors, user-reported issues, recurring friction (with citations back to the source transcript)
- **Workflow patterns** — repeated multi-step sequences worth promoting to a skill or script
- **Capability discoveries** — undocumented commands / flags / recipes the conductor used in real work
- **User corrections** — meta-rules captured from "no, do it this way" exchanges, suitable to encode in the conductor's `CLAUDE.md`

Output lives at the conductor root and is regenerated on each run:

```
~/.agent-deck/conductor/<name>/
├── FINDINGS.md              # raw synthesis of the latest run
├── CAPABILITIES.md          # curated inventory (you edit; survives runs)
├── analysis-manifest.json   # tracking — sha + line counts + analyzer session IDs
└── analysis/                # scripts, prompts, distilled transcripts, per-transcript reports
```

### Quick start

```bash
SKILL_DIR="<base directory shown when this skill was loaded>"
SELFIMP="$SKILL_DIR/scripts/self-improvement"

# Phase 1 — distill all transcripts for one conductor (Python, no LLM, ~1 min)
mkdir -p ~/.agent-deck/conductor/<name>/analysis/distilled
for f in ~/.claude*/projects/-home-*-agent-deck-conductor-<name>/*.jsonl; do
  sid=$(basename "$f" .jsonl | cut -c1-8)
  python3 "$SELFIMP/distill.py" "$f" ~/.agent-deck/conductor/<name>/analysis/distilled/$sid.md
done

# Phase 2 — analyze + synthesize (spawns agent-deck sub-sessions, ~30 min, ~$5)
cp -r "$SELFIMP/prompts" ~/.agent-deck/conductor/<name>/analysis/
bash "$SELFIMP/run-analyzers.sh"   # paced, resumable via manifest

# Phase 3 — file issues from FINDINGS.md (interactive; never auto-files)
bash "$SELFIMP/file-issues.sh"
```

### Privacy

Three layers run before anything leaves the box: regex sanitize → AI sanitizer session → independent AI auditor session. The auditor must verdict `SAFE_TO_SHARE` before the filer will submit. Each layer covers the others' blind spots (regex catches tokens / IPs / paths; AI catches contextual names; auditor catches what the first two missed with fresh eyes). Human review is non-negotiable — the filer prints the exact `gh` command and waits for `[f]` before running it.

### Constraints

- All LLM work happens in spawned agent-deck sub-sessions — never via the Anthropic SDK directly. This dogfoods agent-deck and uses your Claude Max plan.
- Sequential with 30-60s pacing between launches — rate-limit safe.
- Manifest-based resume — re-runs only process new or grown transcripts.

### Deep dive

For the full architecture, output schemas, lessons learned from real runs, and per-script reference, see [references/self-improvement.md](self-improvement.md).

## Goal (goal-driven worker autonomy)

**Use when:** user says "pursue this", "set a goal", "make it work until done", "nudge the agent", "stop me having to message it again", or describes wanting an agent to keep working autonomously toward a specific goal without manual re-prompting.

A complementary layer on top of [Self-Improvement](#self-improvement). Self-improvement is *post-hoc* analysis. Goal is the *live* mechanism that prevents the kinds of stalls self-improvement keeps surfacing — specifically the FINDINGS pattern where a conductor's hourly cron fires 18 times with identical `[STATUS]` replies and no actual progress.

### The core idea

Three entities, never collapsed:

| Entity | Job | Restriction |
|---|---|---|
| **Worker** | Take one bounded step per cycle, write a progress receipt | May NOT decide it's done. May NOT escalate. |
| **Verifier** | An external shell command — runs the done-condition independently | NOT an LLM. NOT the worker's self-assessment. |
| **Manager** | Small Python daemon (cron'd) — runs the verifier, reads receipts, nudges the worker, escalates to user when stuck | NOT involved in doing the work |

Separating these three concerns is what prevents the "agent keeps reporting status but never finishes" failure mode the FINDINGS captured.

### Done-conditions must be shell commands

Examples that work:
- `gh release view v1.6.0 -R asheshgoplani/agent-deck --json publishedAt | jq -e '.publishedAt != null'`
- `gh pr view 890 -R asheshgoplani/agent-deck --json mergedAt | jq -e '.mergedAt != null'`
- `test -s /tmp/report.csv && [ "$(wc -l < /tmp/report.csv)" -gt 100 ]`

Examples that DON'T work:
- "Get this working" → not testable
- "Make the code better" → not measurable
- "Worker says it's done" → self-judgment (the bug we're avoiding)

### Quick start (Phase 1 — hand-wired proof)

The full spec is in [references/goal.md](goal.md). For early use, follow Phase 1:

1. Write a goal JSON at `~/.agent-deck/goals/<id>.json` (schema in the deep-dive doc).
2. Spawn the worker with the contract prompt (template in the deep-dive doc).
3. Run the manager script manually every few minutes to check + nudge.
4. After one real goal completes successfully, graduate to Phase 2 (CLI wrapper) and Phase 3 (cron'd daemon).

### Future CLI surface (Phase 2)

```bash
agent-deck goal \
    --goal "Ship agent-deck v1.6.0" \
    --done 'gh release view v1.6.0 --json publishedAt | jq -e ".publishedAt != null"' \
    --check-every 5m \
    --max-idle 1h \
    --escalate-after 3 \
    --max-cycles 24

agent-deck goal list           # active goals + state
agent-deck goal show <id>      # full JSON dump
agent-deck goal tail <id>      # tail the worker's task-log.md
agent-deck goal cancel <id>    # stop the worker
agent-deck goal resume <id> "<hint>"  # send context-rich hint, reset nudge counter
```

### Deep dive

For the full design — three-entity model, registry schema, worker contract prompt, manager loop pseudocode, nudge generator, escalation bundle, done-condition guidelines, failure modes, implementation phases, and the verification this closes the FINDINGS 18-hour stall — see [references/goal.md](goal.md).

## Trust-but-Verify

**Use when:** A conductor or worker reports any non-trivial completion claim — "PR is merge-ready", "release shipped", "tap updated", "comment posted", "goal done", "bulk drain complete". Treat all such claims as unverified until an independent verifier session has hit ground truth.

### The rule

For ANY non-trivial done-claim, spawn a **separate** Claude session (not the same conductor, not the same worker) whose job is to re-derive the claim from primary sources. The verifier MUST hit:

- Live state (test runs, GitHub API, release artifacts, file contents on disk)
- Not transcripts. Not the original worker's self-report. Not a same-session review.

Same-session reviewers carry the same blind spots that produced the claim. A separate session re-reads the world from scratch.

### Claim → verifier mapping

| Done-claim | Independent verifier MUST run |
|---|---|
| "PR is merge-ready" | `gh pr checkout <N> && go test -race ./...` AND `gh pr view <N> --json mergeable,statusCheckRollup` |
| "release shipped" | `gh release view <tag> --json assets` AND probe each download URL (HTTP 200, non-empty body, correct content-type) |
| "brew tap updated" | `gh api repos/<owner>/homebrew-tap/contents/Formula/agent-deck.rb` — confirm the SHA + URL match the new release |
| "comment posted" | `gh issue view <N> --comments` (or `gh pr view <N> --comments`) and string-match the expected body |
| "goal worker done" | The manager's `done_cmd` must return `rc=0` AS PART OF the verify cycle — the worker's self-report alone never closes a goal |
| "bulk drain complete" | Spawn a read-only AUDIT worker that enumerates the residual open items (open issues, open PRs, pending releases) and diffs them against the claimed-closed set |

### How to spawn the verifier

Use `launch-subagent.sh` from a different session than the one making the claim (peer session, not child of the claimant). Block with `--wait` so the conductor gets the verifier's answer before reporting up:

```bash
$SKILL_DIR/scripts/launch-subagent.sh "Verify PR #885" \
  "Independently verify PR #885 is merge-ready. Run \`gh pr checkout 885 && go test -race ./...\` and \`gh pr view 885 --json mergeable,statusCheckRollup\`. Report PASS only if BOTH succeed; otherwise report the exact failing tests + the mergeable state." \
  --wait --timeout 600
```

Key points:
- **Verifier prompt asks for primary-source commands, not opinions.** The verifier reports what the commands returned, not what it thinks.
- **`--wait` is mandatory.** Fire-and-forget defeats the purpose — the claim ships before the verifier answers.
- **One verifier per claim.** Don't fold three claims into one verifier; that re-introduces the blind-spot pattern at a smaller scale.
- **Verifier MUST be a peer (or root) session**, not a child of the claimant. A child of the claimant inherits the claimant's working state and is structurally closer to "same blind spots".

### Concrete examples from 2026-05-18 (the day this section was baked in)

1. **PR #885 over-claim.** A worker review reported "merge-ready; clean diff". Independent local re-test (`gh pr checkout 885 && go test -race ./...`) caught 2 failing tests. The reviewer had grepped the diff for obvious issues but never ran the suite. Lesson: code-review confidence ≠ test pass.

2. **ux-rethink-path verdict false-positive.** The ux-rethink-path worker reported "merge as-is; clean diff" for #885. User asked for a second look; a fresh peer verifier caught the same 2 failing tests the original worker missed. Lesson: reviewer + author in the same session-tree share priors. A peer session re-derives.

3. **Goal framework "metronome wakes".** The conductor's hourly wake cycles were firing `[STATUS]` replies with no actual work — the wake-loop had degenerated into a heartbeat instead of a do-work loop. User had to flag it manually. Lesson: a worker that hasn't run a ground-truth check since the last wake is not a working worker, it's a metronome. Bake the priority-0 check (below) into the wake template so this can't recur.

### Wake-loop priority 0 (mandatory)

The goal worker contract prompt at [scripts/goal/prompts/worker.md](../scripts/goal/prompts/worker.md) now begins each wake with **PRIORITY 0**: before reporting status or taking the next bounded step, run **one ground-truth verifier check** against any non-trivial claim made in the previous wake's receipt. If no claim was made last wake, skip priority 0 and go to step 1.

This converts a metronome wake into a do-work wake. See the worker template for the exact wording the cycle uses.

### Anti-patterns

- ❌ "I reviewed my own diff and it looks clean" — same-session review.
- ❌ "Codex agreed with me" — consult is brainstorming, not verification. Codex did not run your tests.
- ❌ "The PR's CI is green" alone — CI lag, flaky tests, or test-skip can hide real failures. Pair CI with a local re-run.
- ❌ "Bulk close all 200 — done." — without an audit pass, you don't know how many silently failed.
- ❌ Fire-and-forget verifier — by the time it answers, the conductor has already shipped the claim upstream.

### When you don't need a verifier

For trivial mechanical actions where the action IS its own verification (and the claim is "I did X"):
- File edit + git diff visible → diff IS verification
- `agent-deck list` reporting current sessions → list IS verification
- Reading a file and quoting its contents → quote IS verification

The verifier requirement attaches to claims about external mutable state: PRs, releases, comments, deployments, bulk operations.
