{{include:verify-preamble.md}}

# Consolidated verification report

Recon artifact: `{{RECON_ARTIFACT}}`

Arm artifacts:

{{ARM_ARTIFACTS}}

The fixed recon artifact contract is a JSON object containing deployed identity
(version, revision, and digest), environment, licensing state, authorized
scope, claim, freshness cutoff, failure-classification rules, provenance,
timestamps, a boolean completion field, and an arms array. Every arm entry must
contain its stable arm ID, question, producer, artifact path, expected schema,
deciding fields, and freshness requirement. Before consuming the arms array or
any other recon value, validate this fixed contract, confirm provenance for the
run identified in the preamble, require the completion field to be true,
confirm successful producer publication, and enforce the preamble's freshness
cutoff.

Only after recon passes those checks, validate every required arm artifact
against the schema and provenance declared by recon. Confirm that its named
producer completed publication, its completion field is true, and its evidence
meets the declared freshness requirement. If any required recon or arm artifact
fails schema, provenance, producer-completion, or freshness validation, the
report outcome must be `inconclusive`; it can never be `pass`. Do not infer
success from a path or partial file. After validation, extract only the deciding
fields where possible.

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
