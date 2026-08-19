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

Write the arm schema you settled on to `{{ARM_SCHEMA_PATH}}`, in whatever
format it prescribes for the arms (JSON, YAML frontmatter, a headed Markdown
document — pick what the measurements actually produce). That file is the one
authority every arm producer and the report child validates against, so state
the file format, the required field names, and the container explicitly. Do not
leave the arm format implicit in prose.

Write one schema-consistent JSON document atomically to `{{ARTIFACT_PATH}}`.
The JSON must contain the deployed identity and licensing details, environment,
scope, claim, freshness cutoff, failure-classification rules, and the complete
arm definitions above, plus the path of the arm schema you just wrote. Include
provenance and timestamps plus an explicit completion field set to true in the
staged content only after all recon work is complete, immediately before the
final atomic rename. Successful publication
of that staged document is producer completion. Then report the artifact path
and completion; do not begin an arm or implementation.
