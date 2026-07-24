# Run retrospectives

One file per orchestrate/fleet run, written by the conductor session at the
end of the run (see the Retrospective section in
`skills/orchestrate/SKILL.md`). Purpose: accumulate real-world evidence of
agent-deck bugs, skill friction, and tiering outcomes so both improve from
every run instead of relying on memory.

- Filename: `<date>-<run-id>.md` (e.g. `2026-07-24-run-fix-auth.md`).
- Conductors write these files but never commit them — review, then commit
  or discard.
- A recurring issue across retros is the strongest signal something
  deserves a fix or a skill edit; retros should cross-reference earlier
  retros that report the same issue.
- Periodically triage: promote confirmed items into GitHub issues or skill
  edits, then delete or mark the retro entries as filed.
