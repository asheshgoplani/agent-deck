{{include:verify-preamble.md}}

# Independent measurement arm `{{ARM_ID}}`

Answer only this question, without reading another arm's conclusions:

{{QUESTION}}

Run these measurement commands exactly as authorized:

{{MEASUREMENT_COMMANDS}}

For every command, record the exact command, exit status, timestamps, and the
deciding evidence. Preserve raw evidence needed to diagnose any failure.

Write the result atomically to `{{ARTIFACT_PATH}}` using exactly this expected
schema:

{{ARTIFACT_SCHEMA}}

Populate and call out these deciding fields:

{{DECIDING_FIELDS}}

The schema-valid artifact must include the arm ID and question, run and system
provenance, producer identity, start and completion timestamps, command exits
and evidence, and an explicit completion field that becomes true only after
measurement and the atomic write are complete.

If an external measurement fails, retain its first command, exit status, and
evidence and diagnose product, harness, environment, or licensing causation
before rerunning. Allow at most one clean rerun. Record both attempts without
overwriting the first. A repeated product-behavior failure is a defect; a
repeated harness, environment, or licensing failure is inconclusive. Finish by
reporting the artifact path and producer completion; do not implement a fix.
