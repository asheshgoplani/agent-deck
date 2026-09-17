# Note for the contributor (PR #2085)

Thanks for the shell-completion work — it's a genuinely useful feature and the test coverage is thorough.

The PR had drifted behind `main` and showed as conflicting (CI had also never run on it, only the intake check). We rebased it forward for you so it's clean against current `main`:

- Kept all four of your commits as-is (authorship untouched).
- Resolved two small conflicts on top:
  - `cmd/agent-deck/main.go`: `main` had grown a few extra entries in the `commandRegistry` map (`--version`, `-v`, `--help`, `-h`, `telemetry`) since you branched. Merged those with your `completion`/`__complete` entries — nothing from either side was dropped.
  - `skills/agent-deck/references/cli-reference.md`: both your branch and `main` added a new doc section right after "migrate-paths". Kept both, in the order update-docs then your Shell Completion section.
- `README.md` merged automatically with no manual changes needed.

Verified after rebase: `go build ./...` and `go vet ./...` are clean, and the full completion test suite (`cmd/agent-deck` package, all `Completion`/`Complete` tests plus the rest of the package) passes in a sandboxed Docker container. Only the zsh/fish shell-syntax subtests skip, because those shells aren't installed in the bare `golang:1.25` test image — that's an environment gap, not something wrong with your code.

Nothing in your implementation needed changing. This is purely a rebase to clear the conflict flag so it can go through review/CI cleanly.
