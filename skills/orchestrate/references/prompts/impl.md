{{include:preamble.md}}

Task: {{TASK_TITLE}}

{{SPEC_BLOCK}}

Work strictly in this worktree on the current branch. Do, in order:
1. Install dependencies from the frozen lockfile (never regenerate it).
2. Run the FULL test suite once BEFORE changing anything and record the
   baseline. If something already fails, note it and leave it alone — you
   are accountable only for introducing no NEW failures. List the baseline
   failures in your final summary ("baseline: none" if all green) — the
   reviewer will be given that list. If the repo has no test suite, say so
   and lean on the lint/build checks plus the e2e verification instead.
3. Implement the task test-first (`tdd`), debug failures at the root (`debug`),
   and gate every completion claim on fresh evidence (`verify`).
4. Run the FULL test suite; no new failures versus the baseline. Also run
   the repo's lint/format/build checks — whatever CI runs — and fix what
   they flag on your changes.
5. Verify the change end-to-end by actually driving the app — not only tests.
   For browser work use an isolated browser instance (Playwright-style), not
   a shared Chrome — other tasks may be driving browsers in parallel.
6. Only if the change affects UI: capture before/after screenshots into
   {{RUN_DIR}}/{{TASK_SLUG}}/ using descriptive names (before-<what>.png,
   after-<what>.png). Never commit them, never mention them or that
   directory in any commit message, and take a screenshot of the final
   working state. Describe in words what each screenshot shows — the
   conductor supervising you never opens them.
7. Commit your work in clear logical commits. Do NOT push yet.

Keep your context lean: delegate broad exploration (find-the-code sweeps,
"where is X handled" questions) to subagents so file dumps land outside your
context; read test-output tails rather than full runs; never cat large files
or full logs when a targeted read answers the question.
