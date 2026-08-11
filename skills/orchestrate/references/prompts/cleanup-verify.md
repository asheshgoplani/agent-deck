{{include:delegated-task-preamble.md}}

# Cleanup verification

Repository root: `{{REPO_ROOT}}`
Base ref: `{{BASE_REF}}`
Approved candidate list: `{{CANDIDATE_FILE}}`
Cleanup result: `{{RESULT_FILE}}`

Independently verify the cleanup. Run read-only commands only. Cross-check
every approved candidate against the result artifact and live Git state;
confirm removed worktrees are no longer registered, removed branches are
absent, refused candidates remain intact, every claimed removal was merged
into the base ref, and the main checkout has no new tracked or staged changes.
Do not repair anything.

Write the evidence and exactly one terminal line to `{{VERDICT_FILE}}`:

- `VERDICT: clean` only when every result is accurate and the live state is
  safe and consistent.
- `VERDICT: fix-needed` for any mismatch, unsafe removal, unexpected residual,
  or main-checkout change.
