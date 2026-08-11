{{include:delegated-task-preamble.md}}

# Serialized cleanup execution

Repository root: `{{REPO_ROOT}}`
Base ref: `{{BASE_REF}}`
Approved candidate list: `{{CANDIDATE_FILE}}`
Result artifact: `{{RESULT_FILE}}`

Execute only the exact cleanup candidates in the approved candidate list, one
at a time. Do not discover, infer, add, or broaden targets. Before each
mutation, resolve the exact registered worktree path and branch, confirm the
worktree has no tracked or staged changes, and confirm the branch tip is an
ancestor of the base ref. Refuse any candidate that is dirty, unregistered,
missing required identity, not merged into the base ref, or otherwise differs
from the approved row. Never force deletion.

For an accepted candidate, remove its registered worktree with Git's worktree
command and delete its branch with the safe deletion mode. Record every row as
removed or refused with the deciding evidence. Write the result atomically to
the result artifact, using a temporary sibling followed by rename. Do not edit
tracked files, merge branches, or touch any target absent from the candidate
list.
