# Task 13 — Verification prompt templates

**tier: mid**  
**Parallelism:** strictly after task 12 and before task 14.

## Approved design extract (verbatim)

> Add prompt templates for recon, measurement arms, and the consolidated report. Reuse the existing rotation and handoff mechanism.
>
> Machine-readable artifacts may be read directly only after validating their expected schema, provenance, producer completion, and freshness. Extract only the deciding fields where possible.
>
> For flaky external measurements, preserve the first failure evidence, diagnose it, and permit one clean rerun by default. A second failure terminates as a defect or inconclusive result, distinguishing product behavior from harness, environment, and licensing failures.

## Change

Create `verify-preamble.md`, `verify-recon.md`, `verify-arm.md`, and `verify-report.md` in `skills/orchestrate/references/prompts/`. The latter three start `{{include:verify-preamble.md}}`.

Preamble forbids brainstorming/redesign/implementation, limits writes to assigned artifact, preserves first-failure evidence, requires producer completion, and uses `{{RUN_ID}}`, `{{SCOPE}}`, `{{FRESHNESS_CUTOFF}}`. Recon uses `{{SYSTEM}}`, `{{CLAIM}}`, `{{ENVIRONMENT}}`, `{{ARTIFACT_PATH}}` and requires deployed version/revision/digest/licensing plus arm ID/question/producer/path/schema/deciding fields/freshness and JSON completion. Arm uses `{{ARM_ID}}`, `{{QUESTION}}`, `{{MEASUREMENT_COMMANDS}}`, `{{ARTIFACT_PATH}}`, `{{ARTIFACT_SCHEMA}}`, `{{DECIDING_FIELDS}}`; requires command/exit/evidence, atomic schema-valid artifact with provenance/timestamps, diagnosis plus at most one clean rerun, first-failure preservation, repeated-failure classification, completion. Report uses `{{RECON_ARTIFACT}}`, `{{ARM_ARTIFACTS}}`, `{{REPORT_PATH}}`; validates schema/provenance/completion/freshness before deciding fields, adjudicates conflicts, records reruns and exactly `pass|defect|inconclusive`, with terminal semantics from task 12. Use no other brace placeholders.

## Acceptance criteria

- All four templates render with include/substitution.
- Artifact trust, bounded retry, and no-implementation preamble are explicit.

## Verification

Render each operational template with all keys through `bash skills/orchestrate/references/prompts/render.sh`; expected exit 0. Run `rg '\{\{[A-Z_]+\}\}' <rendered>`; expected exit 1 (no unresolved placeholder).

## Interfaces

consumes:
- task 12 terminology/phase contracts
- `skills/orchestrate/references/prompts/render.sh`

produces:
- four named template files
- exact placeholder sets specified above, consumed by task 14

## Record (append-only)

