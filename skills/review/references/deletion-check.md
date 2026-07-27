# Deletion check

You are checking one thing: did removed code carry behavior that nothing re-established, and that was not intentionally retired?

## When this layer runs

Only when the diff removes meaningful code: deleted functions, branches, guards, validation, cleanup, or tests. Pure renames, pure moves, and formatting-only changes are not deletions for this purpose — skip this layer for those.

## Inputs you get

- The diff.
- The full post-change content of every file the diff touches.
- Read access to the rest of the repo.

## Procedure

1. List every removed *behavior*, not every removed line: a guard, a validation, a fallback, a cleanup step, a log or metric emission, a test case.
2. For each removed behavior, search the post-change tree for a replacement — grep the symbol name, the error string, or the call site that used to invoke it.
3. Classify each one:
   - **re-established elsewhere** — a replacement does the same job; drop it, no finding.
   - **intentionally retired** — the diff or a commit message states the behavior is gone on purpose; drop it, but say so in one line.
   - **silently lost** — no replacement, no stated intent; this is a finding.
4. Removed tests are findings unless the code they covered was also removed in the same diff.

## Output format

A numbered list. This is the only layer that self-rates:

```text
N. <file>:<line-in-old-file> — confidence: high|medium|low — what was
   removed, what searching for a replacement turned up, and what breaks
   if nothing re-established it.
```

- `confidence: high` — you found the consumer that still needs the removed behavior.
- `confidence: medium` — you found no replacement, but also no live consumer that depends on it.
- `confidence: low` — inference from shape alone (e.g. a guard that looked defensive, with no confirmed trigger).

These are inferences, not certainties — state that plainly, which is why this layer rates itself.

## Severity ban

Do not assign severity. Severity is decided at merge.

## Empty result

An empty numbered list is a valid, expected outcome. If nothing qualifies, say exactly:

`No silently-lost behavior found.`

Do not invent a finding to have something to report.
