# Contributing and development

[Back to the README](../../README.md) . [Guide index](README.md)

## Contributing: start here

agent-deck is actively maintained by [Ashesh](https://github.com/asheshgoplani), and it welcomes both contributors and co-maintainers. PRs here don't sit: every incoming PR is validated (applied, built, tested) within about a day, and good ones merge in the next release batch. Recent releases have shipped dozens of community fixes.

Beyond one-off PRs, we're looking for 1-2 regular co-maintainers: people who want to own an area (a tool integration, the TUI, the web view, CI) and help triage and review. The validation pipeline does the heavy lifting; maintainers steer.

To get started:

1. Read [CONTRIBUTING.md](../../CONTRIBUTING.md): how the pipeline works, what makes a PR land fast, and the house rules.
2. Contributing with an AI agent? Point it at [.github/INTAKE.md](../../.github/INTAKE.md), the machine-readable intake spec — or the [contributor workflow](../../skills/agent-deck/references/contributing.md) in the agent-deck skill, which walks an agent (or you) through building, testing, and shaping a clean PR.
3. Comment on an issue before starting work so we can tell you if someone (human or agent) is already on it.
4. Look for issues labeled [`good first issue`](https://github.com/asheshgoplani/agent-deck/issues?q=is%3Aissue+is%3Aopen+label%3A%22good+first+issue%22): curated to be small and self-contained.

Looking for something specific to work on? [docs/ROADMAP.md](../../docs/ROADMAP.md) lists ideas and follow-ups that are real but not yet scheduled.

If you've had a couple of PRs land here and want to help steer, open an issue titled "maintainer: your area". We'd love the help.

## Development

```bash
make build          # Build
make lint           # Lint
make verify-docker  # CI Go checks (gofmt, build, vet, lint, race tests) in Docker
```

Tests start real tmux servers and processes: never run `go test` on your host. See [CONTRIBUTING.md](../../CONTRIBUTING.md) for the sandboxed self-check.
