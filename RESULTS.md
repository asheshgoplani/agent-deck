# Issue #1977 results

PR: https://github.com/asheshgoplani/agent-deck/pull/2054

## Reproduction evidence

Reproduced on current `main` commit `47bb2103` in `golang:1.25` containers before importing any implementation. The new regression tests failed as follows:

- `Match("omp")`, `Match("omp --model sonnet")`, and `Match("/usr/local/bin/omp --continue")` returned `shell`; `IsBuiltin("omp")` was false.
- `DefaultRawPatterns("omp")` returned nil.
- A captured OMP approval pane beginning `Allow tool: bash` was not classified as waiting.

Command:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.25 sh -c 'go test ./internal/session -run TestIssue1977_OMPIsRecognizedAsBuiltin -count=1; go test ./internal/tmux -run TestIssue1977_OMP -count=1'
```

## Root cause

OMP was absent from the canonical built-in registry, so registry matching necessarily reached the shell fallback (`internal/session/builtins.go:56`). It also had no default status preset (`internal/tmux/patterns.go:149`) and no OMP arm in the independent readiness detector, which therefore used shell prompt glyphs (`internal/tmux/detector.go:91`). Finally, the generic model surface had no OMP-backed persistent options or command-builder consumer (`internal/session/instance.go:9340`, `internal/session/omp.go:81`).

## Fix

- Registered `omp` as a basename/token-matched built-in, avoiding substring false positives such as `compass`.
- Added the approved adapter surfaces from reporter @khiladisngh's `feat/omp-native-support` branch: instance-scoped session directories, resume/restart, JSONL fork, environment/config, skills, TUI/Web discovery, and captured v17.3.8 status patterns.
- Added an OMP-specific readiness arm that gives the captured busy marker priority, recognizes approval prompts, and otherwise preserves the previous generic fallback.
- Added persistent per-session model options and `[omp].default_model`; the command builder emits shell-escaped `--model` before lifecycle flags.
- Added `docs/tools/omp.md` and updated the bundled Agent Deck skill configuration reference.

## Proof

The originally failing regression tests now pass. Additional proof:

- `TestOMPLifecycle_LaunchSendRestart` passed in a Go 1.25 container with tmux installed. The fake binary proves launch, instance/model propagation, prompt delivery, response, restart, and resume from the same scoped JSONL directory.
- Targeted OMP session, registry, tmux detector/pattern, and UI tests passed.
- Required build/vet passed:

```sh
docker run --rm -v "$PWD":/src -w /src golang:1.25 sh -c 'git config --global --add safe.directory /src && go build ./... && go vet ./...'
```

The prescribed screenshot gauntlet ran and produced frames under `/tmp/iss1977-proof.ZQLNTE/shots`, but the script hardcodes `/usr/local/bin/agent-deck` and host permissions prevented replacing that binary with the branch build. The run returned cached frames from the installed v1.14.0 binary, so they are explicitly not claimed or attached as proof of this patch. The changed OMP picker/settings/setup surfaces are covered by targeted UI tests.

An attempted full run of `internal/session`, `internal/tmux`, and `internal/ui` exposed unrelated environment failures: the base image lacked tmux, and a read-only-directory test behaves differently as root. Targeted tests were rerun with tmux installed and passed; build/vet passed separately as required.
