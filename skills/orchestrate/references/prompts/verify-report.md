{{include:verify-preamble.md}}

# Consolidated verification report

Recon artifact: `{{RECON_ARTIFACT}}`

Arm artifacts:

{{ARM_ARTIFACTS}}

Before reading any deciding field, validate every artifact against the schema
and provenance declared by recon. Confirm that its named producer completed,
its completion field is true, and its evidence is fresh enough for recon's
cutoff. Reject or classify as inconclusive any artifact that cannot pass these
trust checks; do not infer success from a path or partial file. After validation,
extract only the deciding fields where possible.

Adjudicate conflicting arm evidence explicitly instead of selecting the
convenient result. Record each first failure, its diagnosis, whether the single
allowed clean rerun occurred, both attempts' evidence, and the repeated-failure
classification. Distinguish product behavior from harness, environment, and
licensing limitations.

Write the consolidated report atomically to `{{REPORT_PATH}}`. Include deployed
version, revision and digest; environment and licensing state; authorized
scope; each arm ID, question, evidence, and artifact-validation result;
contradictions and their adjudication; reruns; provenance; timestamps; and an
explicit completion field.

Record exactly one outcome: `pass`, `defect`, or `inconclusive`. A `pass` is
terminal: make no edits, pull request, CI run, or deployment. A `defect` enters
delivery only when it is inside the authorized scope. An `inconclusive` result
is terminal and must state what prevented a trustworthy decision; do not claim
success or retry indefinitely. Report the completed report path and outcome.
