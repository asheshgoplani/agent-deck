# Task 14 — Verification workflow documentation tests

**tier: mid**
**Parallelism:** strictly after tasks 12 and 13.

## Approved design extract (verbatim)

> Add prompt templates for recon, measurement arms, and the consolidated report. Reuse the existing rotation and handoff mechanism.
>
> Machine-readable artifacts may be read directly only after validating their expected schema, provenance, producer completion, and freshness. Extract only the deciding fields where possible.
>
> For flaky external measurements, preserve the first failure evidence, diagnose it, and permit one clean rerun by default. A second failure terminates as a defect or inconclusive result, distinguishing product behavior from harness, environment, and licensing failures.

## Change

Create `cmd/agent-deck/orchestrate_verification_docs_test.go`, following `peer_messaging_docs_test.go`.

`TestOrchestrateSkillDocumentsDeployedVerification` reads `../../skills/orchestrate/SKILL.md`; asserts verification heading precedes per-task pipeline and checks sixth entrance, four phases, artifact validation terms, deciding fields, one-rerun bound, three outcomes, no-change pass, and conditional gh. `TestOrchestrateVerificationPromptTemplatesRender` skips only if `exec.LookPath("python3")` fails; runs real `render.sh` for recon/arm/report with every task-13 key; asserts exit 0, preamble anti-implementation text, no unresolved uppercase brace token, and phase-specific terms. Read source templates and assert includes on exactly the three operational templates.

## Acceptance criteria

- Removal/misordering of any required contract fails a test.
- Real renderer expands every template; only missing python3 can skip.

## Verification

```sh
go test ./cmd/agent-deck -run '^TestOrchestrate(SkillDocumentsDeployedVerification|VerificationPromptTemplatesRender)$' -count=1 -v
```

Expected after red/green: two `PASS`, or renderer `SKIP` only for absent python3; exit 0.

## Interfaces

consumes:
- tasks 12–13 skill section, templates, exact placeholders, and `render.sh`

produces:
- `cmd/agent-deck/orchestrate_verification_docs_test.go`

## Record (append-only)
