# Contributing to agent-deck

Thanks for being here. agent-deck ships community fixes in nearly every release, and contributors like Pan Luo, dolbb, maxfi, and jerodvenemafm have already made the tool measurably better. We want your PR to be next.

This page tells you how the project actually works, what makes a PR land fast, and where to start. If you are an AI agent contributing on a human's behalf, read [.github/INTAKE.md](.github/INTAKE.md): it is the field-level contract, and following it means you pass intake on the first try.

## How this project works (the honest version)

agent-deck has one human maintainer and a fleet of AI agents that do the heavy lifting on intake. That is not a gimmick, it is the reason your PR does not sit for weeks:

- **Every PR gets validated within about a day.** The fleet applies your diff, builds it, runs the sandboxed test suite, and checks merge-cleanliness against main. You get a structured comment with the result.
- **Merges are always human.** The bar is: two independent AI reviews clean, CI green, and the maintainer lands it. No bot ever merges or closes a good-faith PR on its own.
- **A rejected diff is not a rejected idea.** If your code cannot land as-is but the problem is real, the fleet may reimplement the intent, and you get credit in the commit and changelog. What we value most is a well-evidenced problem.

## What makes a PR land fast

1. **A targeted diff.** One problem per PR. Small diffs validate in one pass; a 3k-line diff without prior discussion gets flagged for a conversation first.
2. **Tests.** New behavior needs a test. Run the suite sandboxed (this matters, see below):
   ```bash
   HOME=$(mktemp -d) XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= go test ./...
   ```
3. **Evidence.** For behavior changes, show real output: a terminal capture, logs, or before/after behavior. Mock-only proof is not enough for changes users will feel.
4. **The human need behind the diff.** The single highest-signal thing you can write is one real sentence about what you were doing when you hit this. It is what separates a real fix from a speculative one, and it is the first thing a reviewer reads.
5. **A filled-in PR template.** Problem, why, user impact, evidence, disclosure. It takes five minutes and it is what the validation pipeline reads first.
6. **Perf evidence when you touch hot paths.** If your change affects `list`, `status`, `session output`, startup, or the tmux layer, include simple before/after timing evidence (even `time agent-deck list` runs). Regressions need a stated justification.

## Your first contribution

Good places to start:

- **Issues labeled [`good-first-issue`](https://github.com/asheshgoplani/agent-deck/issues?q=is%3Aissue+is%3Aopen+label%3Agood-first-issue).** Curated to be small and self-contained.
- **Tool integrations.** Readiness and preset patterns for your favorite agent CLI (see `internal/session/` presets for examples).
- **Docs fixes.** Always welcome, always fast to land.

Comment on an issue before starting work so the fleet can tell you if someone (human or agent) is already on it.

## AI-assisted PRs

Welcome, with equal standing, on three conditions: say so in the PR description, include your validation evidence, and be able to answer questions about the code. Undisclosed bulk-generated PRs get closed.

Tell us **which model** in the PR's AI-disclosure section (`claude-opus-4-x`, `gpt-5-codex`, ...). It is a quality signal, not a filter: newer models land clean often, and knowing the model helps us weight and speed up your review. `unsure` is a fine answer.

## Routing

- **Bugs**: open a bug report issue (the form asks for version, repro, and redacted logs, because that is what triage needs).
- **Features**: open a Discussion first. Features that arrive as surprise PRs take longer, not shorter.
- **Questions and setup help**: [Discussions](https://github.com/asheshgoplani/agent-deck/discussions) or the [Agent Deck Discord](https://discord.gg/e4xSs6NBN8), not issues. It keeps the tracker actionable.
- **Security issues**: the private advisory route in [SECURITY.md](SECURITY.md), never a public issue or PR.

## House rules, stated up front so nobody wastes effort

- Maximum 5 open PRs per author at a time.
- Do not edit CHANGELOG.md in your PR; entries are added at landing time.
- Off-topic bulk-content PRs (generated content dumps, mass file additions unrelated to the project) get one kind decline and are closed.
- Keep "Allow edits from maintainers" enabled so urgent fixes can be taken over the line for you.

---

## Development setup

### Prerequisites

- Go 1.24 or later (or Go 1.21+ with automatic toolchain download)
- tmux
- Make

### Getting the code

```bash
git clone https://github.com/YOUR_USERNAME/agent-deck.git
cd agent-deck
git remote add upstream https://github.com/asheshgoplani/agent-deck.git
```

### Building

```bash
make build      # Build binary to ./build/agent-deck
make test       # Run tests (sandbox HOME first; see above)
make lint       # Run linter (requires golangci-lint)
make fmt        # Format code
```

### Running locally

```bash
make dev        # Run with auto-reload (requires 'air')
# or
make run        # Run directly
```

### Debug mode

```bash
AGENTDECK_DEBUG=1 agent-deck
```

## Local-only files (per-developer)

A few files are intentionally not tracked so each developer can keep their own copy:

- `CLAUDE.md` — contributor-specific Claude Code guidance
- `.planning/` — local scratch directory for design docs and drafts

If you use these locally, add them to your own per-clone exclude list:

```bash
cat >> .git/info/exclude <<'EOF'
CLAUDE.md
.planning/
EOF
```

`.git/info/exclude` is local to your clone (never committed) and works exactly like `.gitignore`.

## Branch naming and commits

- `feature/description`, `fix/description`, `perf/description`, `docs/description`, `refactor/description`
- Clear conventional messages: `feat: ...`, `fix: ...`, `docs: ...`, `refactor: ...`
- Run `make fmt` and `make lint` before pushing; CI enforces gofmt.

## Project structure

```
agent-deck/
├── cmd/agent-deck/     # CLI entry point
├── internal/
│   ├── ui/             # TUI components (Bubble Tea)
│   ├── session/        # Session & group management
│   └── tmux/           # tmux integration, status detection
├── tests/              # e2e, eval harness, capability tests
├── .github/workflows/  # CI/CD
└── Makefile            # Build automation
```

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

That is it. Open the PR, fill the template, and the pipeline takes it from there. Thanks for contributing.
