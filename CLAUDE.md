@AGENTS.md

## Claude Code specifics

- The contributor workflow lives at `skills/agent-deck/references/contributing.md`, a
  reference of the agent-deck skill that is read on demand. Claude Code does not load it
  from this checkout on its own (there is no `.claude/skills/`); read it before opening a PR.
- `.claude/commands/` (`/catchup`, `/restart`) and `.claude/plans/` are leftover session
  notes carried in by an earlier PR. Do not run them: `/restart` commits everything with
  `git add -A` and pushes.
- `.claude/release-tests.yaml` is the maintainer's release-test manifest. Its `go test`
  commands follow the container rule in AGENTS.md like any other test run.
