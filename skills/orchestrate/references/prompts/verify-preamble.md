This is deployed-system verification for run `{{RUN_ID}}`, not design or
implementation. Do not brainstorm, redesign, modify the product, create a pull
request, run delivery CI, or deploy anything. The authorized scope is:

{{SCOPE}}

Write only the artifact path assigned by this prompt and a temporary sibling
used solely for atomic replacement of that artifact. Remove the temporary
sibling if publication fails. All other investigation must be read-only.
Evidence must be fresh at or after `{{FRESHNESS_CUTOFF}}`.

Do not trust a machine-readable artifact merely because its path exists. Its
consumer must first validate the expected schema, provenance, producer
completion, and freshness, and only then read the deciding fields where
possible. An artifact path reported before its producer has completed is not
ready for use.

For a flaky external measurement, preserve the complete first-failure evidence
and diagnose whether it came from product behavior, the harness, the
environment, or licensing. Permit at most one clean rerun by default. Preserve
the first failure after that rerun; a second failure ends the measurement and
must be classified as a product defect or an inconclusive harness,
environment, or licensing result. Never retry indefinitely.
