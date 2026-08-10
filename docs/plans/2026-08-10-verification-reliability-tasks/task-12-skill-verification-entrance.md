# Task 12 — Deployed-system verification entrance

**tier: strong**  
**Parallelism:** Group D start; strictly before tasks 13 and 14.

## Approved design extract (verbatim)

> Add a verification entrance to the orchestration skill before pull-request-specific prerequisites.
>
> The flow is:
>
> 1. Recon establishes deployed versions, environment, scope, arm definitions, and artifact contracts.
> 2. Independent measurement arms run in parallel.
> 3. The conductor validates and adjudicates their machine-readable artifacts.
> 4. A consolidated report records evidence and one of three outcomes: `pass`, `defect`, or `inconclusive`.
>
> `pass` is a complete terminal outcome: no edits, pull request, CI run, or deployment is required. `defect` may enter the implementation pipeline only for defects within the authorized scope. `inconclusive` reports what prevented a trustworthy decision without pretending success or retrying indefinitely.
>
> Machine-readable artifacts may be read directly only after validating their expected schema, provenance, producer completion, and freshness. Extract only the deciding fields where possible.
>
> For flaky external measurements, preserve the first failure evidence, diagnose it, and permit one clean rerun by default. A second failure terminates as a defect or inconclusive result, distinguishing product behavior from harness, environment, and licensing failures.

## Change

Edit only `skills/orchestrate/SKILL.md`. Add “deployed-system verification” as the sixth entrance in the bullets and ASCII routing table, routed to recon. Amend `Requires:` so authenticated `gh` is required for delivery/PR entrances, not verification-only work.

Immediately before `## Per-task pipeline`, insert `## Deployed-system verification`. Specify: verification scope/no assumed edit; recon fields (deployed version/revision/digest, environment/licensing, authorized scope, arm IDs/questions, exact artifact paths/schemas, freshness cutoff); parallel independent arms; producer writes artifact, finishes, reports path; conductor validates schema, provenance, producer completion, and freshness before deciding-field reads; contradictions are adjudicated; first flaky evidence is preserved and diagnosed with at most one clean rerun; second failure becomes product `defect` or harness/environment/license `inconclusive`; report contains identity/scope/evidence/validation and exactly `pass|defect|inconclusive`; pass ends without edits/PR/CI/deploy, defect enters delivery only in scope, inconclusive terminates honestly; existing rotation/handoff applies. Make downstream PR language conditional on delivery. Do not alter per-task mechanics.

## Acceptance criteria

- Six entrances and routing are consistent.
- Verification precedes PR stages and specifies four phases.
- Artifact trust, retry bound, outcomes, and no-change pass are explicit.

## Verification

```sh
rg -n 'Deployed-system verification|schema|provenance|producer completion|freshness|one clean rerun|inconclusive|no edits|authenticated.*gh' skills/orchestrate/SKILL.md
git diff --check -- skills/orchestrate/SKILL.md
```

Expected: every concept matches; diff check exits 0.

## Interfaces

consumes:
- `skills/orchestrate/SKILL.md`: entrance bullets/table, `Requires:`, rotation/handoff, insertion before `## Per-task pipeline`

produces:
- `## Deployed-system verification`, sixth entrance routing, and contracts consumed by tasks 13–14

## Record (append-only)

