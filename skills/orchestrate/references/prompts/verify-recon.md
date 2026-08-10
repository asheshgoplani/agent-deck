{{include:verify-preamble.md}}

# Recon

Inspect the deployed system `{{SYSTEM}}` and establish the verification contract
for this claim:

{{CLAIM}}

Target environment: `{{ENVIRONMENT}}`

Record the deployed version, source revision, immutable artifact or image
digest, and licensing state. Record the target environment and authorized
scope, and state what evidence will distinguish product behavior from harness,
environment, and licensing failures.

Define every independent measurement arm with a stable arm ID and the exact
question it answers. For each arm, name its producer, artifact path, expected
schema, deciding fields, and freshness requirement. Require each producer to
finish before its artifact is consumed.

Write one schema-consistent JSON document atomically to `{{ARTIFACT_PATH}}`.
The JSON must contain the deployed identity and licensing details, environment,
scope, claim, freshness cutoff, failure-classification rules, and the complete
arm definitions above. Include provenance and timestamps plus an explicit
completion field set to true only after all recon work and the atomic write are
complete. Then report the artifact path and completion; do not begin an arm or
implementation.
