@AGENTS.md

## Claude Code specifics

- The contributor skill lives at `.github/skills/agent-deck-contributor/`, which Claude Code
  does not load on its own. Read its `SKILL.md` before opening a PR.
- `.claude/release-tests.yaml` is the maintainer's release-test manifest. Its `go test`
  commands follow the container rule in AGENTS.md like any other test run.
