This is EXECUTION of already-approved work, not design. The design exists and
the user approved it; it is linked below and is your requirements. Do not
re-open the design, do not re-brainstorm the spec, do not propose alternative
approaches, and do not wait for design approval — there is no user in this
session to give it. You are writing the plan, so plan-writing skills are fair
game; design skills are not. If you think the spec is actually wrong, stop and
say so in one line; do not redesign around it.

End your final message with the `===AGENTDECK_DONE=== status=<ok|fail>
summary=<one line>` sentinel as the last line.

Read the approved design at {{SPEC_PATH}} and explore the codebase as needed.
Write an implementation plan to {{TASK_DIR}}/plan.md:
ordered, bite-sized tasks; per task: exact file paths, the actual code or
edit, verification commands with expected output, and the interfaces later
tasks rely on. Mark any tasks that are safe to run in parallel (disjoint
files). Tag every task with `tier: mid | strong` — mid when the task is
transcription of code this plan already contains or needs only local
judgment within a clear spec, strong when it makes design decisions. There
is no tier below mid: every executor still has to run the verification
commands and diagnose what the plan did not predict. Assume each task's executor has ZERO context beyond that one task.
Size every task to fit comfortably in a single fresh session's context
window: if completing it would require reading more than roughly 100k tokens
of code, docs, and test output, split it further — a task that blows up its
executor's context costs a handoff mid-implementation.

Then emit one self-contained task file per task at
{{TASK_DIR}}/tasks/task-NN-<name>.md. Each task file must
stand alone for a child that reads nothing else:
- the relevant design-doc extracts EMBEDDED verbatim, never linked;
- acceptance criteria;
- exact file paths and the actual code or edit;
- verification commands with expected output;
- an `## Interfaces` block with `consumes:` and `produces:` — the exact
  names, signatures, and paths this task relies on and hands over, so a
  child that sees only its own file knows its neighbours' names;
- a trailing `## Record (append-only)` section, left empty, for the
  implementer to append its commits, files touched, and concerns.

No placeholders (no TBD / "add error handling" / "similar to task N").

{{TASK_DIR}} is an absolute path OUTSIDE this worktree, in the root checkout,
and it is git-ignored. Write there and only there. Do NOT copy the plan or the
task files into this worktree, do NOT `git add` or commit them, and do NOT run
any git command that changes this branch — the plan is scaffolding for the
run, not a change to the repository. When you finish, `git status --porcelain`
in this worktree must print nothing. Do NOT implement anything.
