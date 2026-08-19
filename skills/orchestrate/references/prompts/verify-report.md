{{include:verify-preamble.md}}

# Consolidated verification report

Recon artifact: `{{RECON_ARTIFACT}}`

Arm artifacts:

{{ARM_ARTIFACTS}}

**The arm schema declared by the conductor is authoritative.** It is at
`{{ARM_SCHEMA_PATH}}`, and it — not this template, and not any shape you infer
from the recon artifact's prose — defines what file format, field names and
container each arm and the recon artifact use. Read it first. Markdown arms are
as valid as JSON arms if that is what it declares.

Every recon and arm artifact must carry the same information regardless of the
format the schema picks: deployed identity (version, revision, digest),
environment, licensing state, authorized scope, claim, freshness cutoff,
failure-classification rules, provenance, timestamps, and a completion marker;
and per arm, its stable arm ID, question, producer, artifact path, deciding
fields, and freshness requirement. Validate the recon artifact against the
declared schema before consuming its arms: confirm provenance for the run
identified in the preamble, require the completion marker to be set, confirm
successful producer publication, and enforce the preamble's freshness cutoff.

Only after recon passes those checks, validate every required arm artifact the
same way. Confirm that its named producer completed publication, its completion
marker is set, and its evidence meets the declared freshness requirement. If any
required recon or arm artifact fails provenance, producer-completion, or
freshness validation, the report outcome must be `inconclusive`; it can never be
`pass`. Do not infer success from a path or partial file. After validation,
extract only the deciding fields where possible.

**A shape mismatch is a question, not a verdict.** If an artifact carries the
required information but not in the shape you expected — a different file
format, a different arm count, fields under different names — that is a
discrepancy to report to the conductor and resolve, NOT grounds for
`inconclusive`. `inconclusive` is reserved for evidence that is missing, stale,
unattributable, or contradictory. Never rule against a run on packaging while
leaving its evidence unchallenged: say plainly which schema you validated
against, what differed, and what the evidence itself shows.

Adjudicate conflicting arm evidence explicitly instead of selecting the
convenient result. Record each first failure, its diagnosis, whether the single
allowed clean rerun occurred, both attempts' evidence, and the repeated-failure
classification. Distinguish product behavior from harness, environment, and
licensing limitations.

Write the consolidated report atomically to `{{REPORT_PATH}}`. Include deployed
version, revision and digest; environment and licensing state; authorized
scope; each arm ID, question, evidence, and artifact-validation result;
contradictions and their adjudication; reruns; provenance; timestamps; and an
explicit completion field. Set the completion field to true in the staged
content only after report construction is complete, immediately before the
final atomic rename. Successful publication of that staged document is
producer completion.

Record exactly one outcome: `pass`, `defect`, or `inconclusive`. A `pass` is
terminal: make no edits, pull request, CI run, or deployment. A `defect` enters
delivery only when it is inside the authorized scope. An out-of-scope `defect`
is terminal: record it without edits, pull request, CI run, or deployment. An
`inconclusive` result is terminal and must state what prevented a trustworthy
decision; do not claim success or retry indefinitely. Report the completed
report path and outcome.
