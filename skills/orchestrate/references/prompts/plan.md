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
Write an implementation plan to docs/plans/{{DATE}}-{{TASK_SLUG}}-plan.md:
ordered, bite-sized tasks; per task: exact file paths, the actual code or
edit, verification commands with expected output, and the interfaces later
tasks rely on. Mark any tasks that are safe to run in parallel (disjoint
files). Tag every task with `tier: cheap | mid | strong` — cheap when the
task is pure transcription of code this plan already contains, mid when it
needs local judgment within a clear spec, strong when it makes design
decisions. Assume each task's executor has ZERO context beyond that one task.
Size every task to fit comfortably in a single fresh session's context
window: if completing it would require reading more than roughly 100k tokens
of code, docs, and test output, split it further — a task that blows up its
executor's context costs a handoff mid-implementation.

Then emit one self-contained task file per task at
docs/plans/{{DATE}}-{{TASK_SLUG}}-tasks/task-NN-<name>.md. Each task file must
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
Commit the plan and the task files to the current branch. Do NOT implement
anything.
