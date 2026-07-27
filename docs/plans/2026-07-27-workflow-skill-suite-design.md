# Workflow Skill Suite — Design

**Date:** 2026-07-27
**Status:** Approved
**Replaces:** the superpowers plugin as the workflow-discipline layer for agent-deck users.

## Motivation

The superpowers plugin currently provides brainstorming, debugging, TDD,
verification, and code-review discipline, but it fights the agent-deck flow:

- Its `subagent-driven-development` skill is structurally the same algorithm as
  `orchestrate` (fresh implementer per task → fresh reviewer → bounded fix loop
  → final review), so children can nest a second review loop inside the
  conductor's.
- Its SessionStart hook injects a "you MUST use skills" mandate into every
  session — including orchestrate children, which then try to brainstorm tasks
  that already have an approved design. `orchestrate` carries hand-written
  defenses against this today.
- Its `writing-plans`/`executing-plans` pipeline is dead weight: in this flow
  the planner child of `orchestrate` writes the plan.
- It writes specs to `docs/superpowers/specs/`, which is commonly gitignored —
  the commit silently no-ops.

This design replaces superpowers with a lean suite of skills shipped in the
agent-deck plugin itself, sharing one review methodology between interactive
use and orchestrated reviewer children, with a child-aware SessionStart hook
that gives executors the opposite instructions from interactive sessions.

Sources mined for method (credited, not copied): obra/superpowers (iron laws,
rationalization tables, reviewer prompt contracts), tam-tools `tam-review`
(orthogonal reviewer layers, severity-at-merge, Demonstration gate), and
BMAD-METHOD v6 (story-file-as-contract, triage buckets, state-as-artifact).

## Decisions (from brainstorming)

- **Scope:** ship in the agent-deck plugin (`skills/`), superpowers
  uninstalled entirely afterwards.
- **Coverage:** design (brainstorming), systematic debugging, TDD,
  verification, code review with adversarial + edge-case + verification-gap
  layers.
- **Flow exit:** tiered — big work hands the approved design to `orchestrate`;
  tiny work is implemented in-session under `tdd` + `verify`.
- **Hook:** child-aware SessionStart hook (needs a small Go change: export
  child markers into the spawned session env).
- **Audience:** build local, PR upstream once battle-tested → everything
  written generically, no DoozyX-specific conventions.

## 1. Inventory & layout

```
skills/
  agent-deck/ session-share/ fleet/ orchestrate/     # existing
  design/SKILL.md                                    # brainstorming, adapted
  review/SKILL.md                                    # thin dispatcher
  review/references/adversarial.md                   # hostile persona, >=10-findings quota, diff-only
  review/references/edge-cases.md                    # mechanical path tracing, implicit branches, JSON contract
  review/references/verification-gap.md              # "would any test fail?", Demonstration gate
  review/references/deletion-check.md                # conditional sub-pass for removed code
  review/references/principles.md                    # DRY / KISS / YAGNI / SOLID + violation smells
  debug/SKILL.md                                     # systematic debugging
  tdd/SKILL.md                                       # red/green/refactor discipline
  verify/SKILL.md                                    # claim->evidence gate
hooks/
  hooks.json                                         # SessionStart -> session-start script
  session-start                                      # child-aware injection (section 5)
```

All new skills are registered in `.claude-plugin/marketplace.json`.

`review/references/` is the shared methodology core: `/review` dispatches the
layers interactively, and orchestrate's fresh-reviewer children are pointed at
the same files by path. One source of truth; cross-skill file references are
already established practice (`orchestrate` says "Read `skills/fleet/SKILL.md`
first").

All content is rewritten, not copied. Skills stay small: leaf disciplines
~60–120 lines, review layers ~40–90 lines each.

## 2. The `design` skill

Kept from superpowers brainstorming:

- Explore project context before anything else.
- Clarifying questions **one per message**.
- 2–3 approaches with trade-offs and a recommendation; YAGNI ruthlessly.
- Design presented in sections, approval after each.
- HARD-GATE: no implementation (and no implementation skill) until the design
  is presented and approved — regardless of perceived simplicity.
- Spec self-review (placeholders, contradictions, scope, ambiguity), then a
  user review gate on the written file.

Changed:

- **Spec location:** honor the repo's visible convention (a `docs/plans/`,
  `docs/specs/`, or similar dir containing prior design docs); default
  `docs/plans/YYYY-MM-DD-<topic>-design.md`. Always committed — and verified
  committed (named check for the gitignore trap: `git status` must show the
  file staged/committed, not ignored).
- **Principles pass:** before presenting the chosen architecture, check it
  against `review/references/principles.md` — does any component exist for a
  requirement nobody stated?
- **Tiered exit** (replaces "invoke writing-plans"): after approval, size the
  work. Multi-task / multi-file / PR-worthy → hand the design doc to
  `orchestrate` (its planner child writes the plan). Genuinely tiny (single
  file, one sitting) → implement in-session under `tdd` + `verify`.
  Borderline → one final question with a recommendation.
- **Child guard:** first block of the skill — a session dispatched as an
  executor (per the hook) does not brainstorm; it follows its task prompt.

Dropped: visual companion, writing-plans/executing-plans, `docs/superpowers/`
paths.

## 3. The `review` skill + shared layers

`SKILL.md` is a thin dispatcher (~80 lines):

1. **Resolve target.** Default: uncommitted changes. Args accept a diff range,
   PR number, branch, or file list. Optional `also consider ...` arg threads
   task-specific concerns into every layer.
2. **Scope.** Code present → all three layers (+ `deletion-check` when the
   diff removes meaningful code). Docs/config only → adversarial only, and say
   why the others were skipped.
3. **Dispatch layers in parallel** as subagents when the Agent tool is
   available; sequential in-context otherwise. Information asymmetry is
   deliberate and stated: the adversarial reviewer gets the diff **only** (no
   spec, no conversation, no repo access — kills anchoring bias); edge-cases
   and verification-gap get the diff plus full post-change file content and
   repo read access (tracing consumers without it manufactures false
   positives).
4. **Merge & dedup.** Same location + same underlying issue = one finding;
   keep the more detailed description; union provenance tags (`[Adversarial]`,
   `[Edge]`, `[V-Gap]`). **Severity is banned at the leaf and assigned only at
   merge** (leaves have by-design information asymmetry). Canonical scale
   everywhere: **critical / major / minor**. Within a severity, findings
   flagged by more layers sort first (free confidence signal).
5. **Tone transform.** Strip the adversarial persona's hostility; reframe as
   observation + concrete fix.
6. **Triage buckets:** `patch` (mechanical, fix now) / `decision-needed`
   (escalate to human or conductor) / `defer` (pre-existing, appended to a
   deferred-work file, out of scope, never extends a loop). "Clean" means *no
   patch or decision-needed items*, and the clean verdict is a mandated exact
   line, never an empty response.

Layer contracts (each file self-contained — a fresh subagent can execute from
the file alone):

- **`adversarial.md`** — hostile persona; "find at least ten issues"; zero
  findings = HALT and re-analyze; checklist explicitly includes principles
  violations per `principles.md` (over-engineering, duplication, SRP breaks);
  descriptions only, no severity.
- **`edge-cases.md`** — pure path tracer, never opines on quality; enumerates
  control-flow boundaries and **implicit branches** (untouched members of
  enums/status sets/sentinels the diff special-cases); strict JSON output:
  `location`, `trigger_condition` (≤15 words), `guard_snippet`,
  `potential_consequence` (≤15 words); `[]` is valid.
- **`verification-gap.md`** — single question: "if this changed behavior
  stopped holding where it's used, would any test fail?" Cheap-triage-first
  whitelist so neutral changes exit in one step; bounded consumer tracing
  (1–3 hops, named stop conditions); the **Demonstration gate** — name the one
  concrete mutation the consumer would observe, or drop the finding;
  anti-fabrication clause — never assert a test exists/passes unless found and
  read.
- **`deletion-check.md`** — did removed code carry behavior nothing
  re-established or intentionally retired? Only layer with a self-rated
  `confidence` field (these are inferences).
- **`principles.md`** (~30 lines) — DRY, KISS, YAGNI, SOLID: one line each on
  meaning plus 2–3 concrete violation smells (needless abstraction layer,
  speculative generality, copy-pasted logic drifting apart, god-object/SRP
  breaks, boolean-flag parameters). Consumed by `design` (architecture pass),
  `tdd` (refactor step), and the adversarial layer.

## 4. Leaf disciplines

**`debug`** (~100 lines). Iron law: *no fixes without root-cause investigation
first*. Four sequential phases: investigate (read the actual error, reproduce,
check recent changes, instrument component boundaries) → pattern analysis
(find a working example, diff against it) → single hypothesis, smallest test,
one variable → fix with a failing test first. **3-failed-fixes circuit
breaker:** after the third failed fix, stop, question the architecture, talk
to the human before attempt #4. Condensed red-flags table. Chains out to
`tdd` (repro test) and `verify` (before claiming fixed).

**`tdd`** (~90 lines). Iron law: *no production code without a failing test
first*; code written first is deleted, not kept as reference. Red → **verify
RED** (watch it fail, for the right reason; an immediately-passing test is
testing existing behavior) → minimal green → **verify GREEN** (clean output,
no warnings) → refactor (checked against `principles.md`). Two gates: before
writing a test, name the production change that would make it fail (can't →
redesign the test); before adding a mock, list the real side effects and never
assert on the mock itself. **Mutation check:** wrong constant, flipped branch,
missing side effect, empty return — a mutation nothing catches means the
behavior is unprotected. Exceptions (prototypes, generated code) require
asking.

**`verify`** (~70 lines). Iron law: *no completion claims without fresh
evidence in this message*. Claim→evidence table: "tests pass" → 0-failure run
now; "build works" → exit 0 (linter passing insufficient); "bug fixed" →
original symptom re-tested; "regression test added" → verified red-green cycle
(revert fix → test must fail → restore); **"child/agent completed" → the VCS
diff, never the agent's success report**. Red flags: "should/probably/seems",
any "Done!"/"Perfect!" before evidence.

These three are standalone (any session, any repo) and are what the hook
nudges orchestrate's implementer children toward.

## 5. Child-aware SessionStart hook

`hooks/hooks.json` registers one SessionStart hook (matcher
`startup|clear|compact` — compaction re-injection keeps discipline alive in
long sessions). The `session-start` script injects one of two small preambles:

- **Child sessions** (~15 lines): "You are a dispatched executor. Your task
  prompt is the contract — do not brainstorm, do not re-open the design, do
  not spawn your own review loop. Disciplines: `tdd` while implementing,
  `debug` on failures, `verify` before reporting done. Report via the done
  sentinel."
- **Interactive sessions** (~15 lines): lean pipeline nudge — feature/change →
  `design`; bug → `debug`; before claiming done → `verify`; `review` on
  demand. No "1% chance → MUST" absolutism.

**Detection requires a small Go change:** at launch, when the new session has
a parent, agent-deck exports `AGENTDECK_ROLE=child` and `AGENTDECK_PARENT_ID`
into the spawned tmux session environment. The hook reads only the env
(`tmux show-environment`): `AGENTDECK_ROLE=child` → executor preamble;
anything else (including non-agent-deck sessions, or no tmux at all) →
interactive preamble, degrading silently. No DB lookups.

## 6. Orchestrate upgrades

Three targeted changes, no restructuring:

1. **Story-file tasks.** The planner child emits
   `docs/plans/<date>-<slug>-tasks/task-NN-<name>.md`, each self-contained:
   relevant design-doc extracts **embedded** (not linked), acceptance
   criteria, exact file paths, and an Interfaces consumes/produces block so a
   child that sees only its own task knows its neighbors' names. Implementer
   children read **only their task file** — a child that never reads the full
   design can't drift from it. Each task file has a small append-only record
   section the child writes (commits, files touched, concerns): audit trail
   without conductor context cost.
2. **Reviewer children run the shared layers.** The fresh-reviewer prompt
   shrinks to: task file + diff range + "execute the layers per
   `skills/review/references/`, write the verdict file." The conductor
   branches on the machine-readable verdict (`clean` / `fix-needed` +
   bucketed findings); `decision-needed` escalates to the user;
   `defer` items append to the run's deferred-work file and never extend the
   loop.
3. **Discipline preambles shrink.** Hand-written "you must TDD/verify" child
   boilerplate becomes one line pointing at the leaf skills — the hook already
   injects the executor preamble. Existing anti-superpowers defenses stay
   (upstream users may still run superpowers).

## 7. Migration, testing, upstream

**Migration:** land the suite → reinstall the agent-deck plugin from the repo
(directory marketplace) → uninstall superpowers at user scope. The
`~/DoozyX/superpowers` checkout stays as reference. One session of
side-by-side sanity (`/design` on something small, `/review` on a real diff)
before superpowers is removed.

**Testing:**

- Discipline skills: pressure-test with subagent scenarios (a subagent handed
  a tempting shortcut — "just fix it quickly, skip the test" — must comply
  with the skill).
- Review layers: golden test — run `/review` on a historical diff with known
  documented bugs from this repo; the layers must find them without
  hallucinating severity.
- Hook: launch one parented child and one plain session; confirm each received
  the correct preamble.

**Upstream genericity:** no DoozyX-specific paths or conventions; spec-dir
detection instead of hardcoding; hook degrades to interactive-preamble
everywhere; the Go change (child env markers) is a generic feature useful
beyond this suite. PR upstream as one unit once battle-tested.

## Out of scope

- Porting superpowers' writing-plans / executing-plans /
  subagent-driven-development / finishing-a-development-branch (orchestrate
  owns that pipeline).
- BMAD-style personas, PRD stacks, sprint/retro rituals, TEA test-scoring
  machinery.
- An external conductor state-machine file for orchestrate (revisit if the
  context-budget bounds from 7bec008a prove insufficient).
- Any change to `fleet` / `session-share` / `agent-deck` skills.
