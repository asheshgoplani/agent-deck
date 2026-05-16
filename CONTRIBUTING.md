# Contributing to Agent Deck

Thank you for your interest in contributing to Agent Deck! This document provides guidelines and information for contributors.

## Getting Started

1. **Fork the repository** on GitHub
2. **Clone your fork** locally:
   ```bash
   git clone https://github.com/YOUR_USERNAME/agent-deck.git
   cd agent-deck
   ```
3. **Add upstream remote**:
   ```bash
   git remote add upstream https://github.com/asheshgoplani/agent-deck.git
   ```

## Development Setup

### Prerequisites

- Go 1.24 or later (or Go 1.21+ with automatic toolchain download)
- tmux
- Make

### Building

```bash
make build      # Build binary to ./build/agent-deck
make test       # Run tests
make lint       # Run linter (requires golangci-lint)
make fmt        # Format code
```

### Running Locally

```bash
make dev        # Run with auto-reload (requires 'air')
# or
make run        # Run directly
```

## Local-Only Files (Per-Developer)

A few files are intentionally not tracked by this repo so each developer can keep their own copy:

- `CLAUDE.md` — contributor-specific Claude Code guidance (untracked in #1002)
- `.planning/` — local scratch directory for design docs, phase plans, RFC drafts

These files are not listed in `.gitignore` so the project doesn't dictate per-developer file presence. If you use these locally, add them to your own per-clone exclude list:

```bash
cat >> .git/info/exclude <<'EOF'
CLAUDE.md
.planning/
EOF
```

`.git/info/exclude` is local to your clone (never committed) and works exactly like `.gitignore` for ignore semantics.

## Making Changes

### Branch Naming

- `feature/description` - New features
- `fix/description` - Bug fixes
- `perf/description` - Performance optimizations
- `docs/description` - Documentation changes
- `refactor/description` - Code refactoring

### Commit Messages

Use clear, descriptive commit messages:

```
feat: add support for custom commands
fix: resolve status detection for OpenCode
docs: update installation instructions
refactor: simplify group management logic
```

### Code Style

- Run `make fmt` before committing
- Run `make lint` to check for issues
- Follow existing code patterns
- Add tests for new functionality

## Pull Request Process

1. **Create a feature branch** from `main`:
   ```bash
   git checkout -b feature/my-feature
   ```

2. **Make your changes** and commit them

3. **Keep your branch updated**:
   ```bash
   git fetch upstream
   git rebase upstream/main
   ```

4. **Push to your fork**:
   ```bash
   git push origin feature/my-feature
   ```

5. **Open a Pull Request** on GitHub

### PR Guidelines

- Provide a clear description of the changes
- Reference any related issues
- Ensure all tests pass
- Update documentation if needed

## Reporting Issues

### Bug Reports

Include:
- Agent Deck version (`agent-deck version`)
- Operating system and version
- tmux version (`tmux -V`)
- Steps to reproduce
- Expected vs actual behavior
- Any error messages or logs

### Feature Requests

- Describe the use case
- Explain why existing features don't solve it
- Provide examples if possible

## Project Structure

```
agent-deck/
├── cmd/agent-deck/     # CLI entry point
├── internal/
│   ├── ui/             # TUI components (Bubble Tea)
│   ├── session/        # Session & group management
│   └── tmux/           # tmux integration, status detection
├── .github/workflows/  # CI/CD
├── Makefile           # Build automation
└── README.md
```

## Testing

- Add tests for new functionality
- Run the full test suite: `make test`
- Tests should be deterministic and not depend on external state

### Debug Mode

Enable debug logging:
```bash
AGENTDECK_DEBUG=1 agent-deck
```

## Questions?

- **Chat:** Join the [Agent Deck Discord](https://discord.gg/e4xSs6NBN8) for questions, workflow discussions, and community support
- **Issues:** [Open an issue](https://github.com/asheshgoplani/agent-deck/issues) for bugs or feature requests

## License

By contributing, you agree that your contributions will be licensed under the MIT License.
