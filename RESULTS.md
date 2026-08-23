# Round-3 Results — PR #2054

Base: `b1252e08a23fe921f2829c4a0c16e4975c4fe1d8` (already even with `origin/main`).
Fix commit: `9f9883df` plus this results-only follow-up. All Go commands ran in
`golang:1.25` containers; no host `go test` was used.

## 1. Built-in launcher aliases

- Reproduction: `Registry.Match("oh-my-pi")` and
  `Registry.Match("npx @oh-my-pi/pi-coding-agent --model opus")` returned
  `shell`.
- Root cause: `internal/session/builtins.go:67` registered only the `omp`
  token.
- Fix: registered the two supported package/launcher substrings while retaining
  token-only matching for bare `omp` (so words such as `component` remain
  non-matches).
- Red-without-fix: in `/tmp/r3-2054/mutation` (parent production plus the new
  tests), the session test group failed to compile because the same group also
  pins the newly introduced structured option contract. The tmux mutation below
  independently produced assertion-level red for both aliases. This is genuine
  contract red, not an environment failure.
- Preserved invariant: short bare `omp` is not substring-matched; ambiguous
  containing words retain shell behavior.

## 2. Initial native fork model/options

- Reproduction: an SSH-target parent with persisted model `review/model`, model
  cycle `fast,slow`, and profile `review` generated a fork command containing
  none of those flags, and the child did not persist those options.
- Root cause: `internal/session/omp.go:122-152` built the fork invocation from
  only the executable, fork source, session directory, and approval default.
- Fix: copy the parent's structured OMP options to the child and append all
  non-session harness arguments to the initial native fork command. The normal
  start/restart builder at `internal/session/omp.go:92-120` uses the same option
  model.
- Red-without-fix: `/tmp/r3-2054/mutation-fork` restores only `omp.go` to the
  parent while retaining current options/tests. `go test ./internal/session
  -run TestOMPForkCarriesModelAndHarnessOptions` failed assertions for missing
  `--model review/model`, `--models fast,slow`, `--profile review`, and missing
  persisted child options (`FORK_MUTATION_EXIT=1`).
- Preserved invariant: forks still use `--fork` and distinct parent/child
  instance-scoped directories; normal restart still defaults to `--continue`.

## 3. tmux detection aliases and false positive

- Reproduction: supported `oh-my-pi` and `npx
  @oh-my-pi/pi-coding-agent` commands returned empty, while `grep omp README.md`
  returned `omp`.
- Root cause: `internal/tmux/tmux.go:861-902` recognized literal `omp` and then
  used a permissive fallback that searched argument text.
- Fix: `isOMPCommand` at `internal/tmux/tmux.go:906-928` recognizes `omp`,
  `oh-my-pi`, and the exact npx package only in command position, including
  environment prefixes; the incidental-text fallback was removed.
- Red-without-fix: parent production plus
  `internal/tmux/omp_round3_test.go` failed four intended assertions: all three
  supported alias forms were missed and `grep omp README.md` false-matched
  (`TMUX_MUTATION_EXIT=1`).
- Preserved invariant: executable paths and environment-prefixed launches are
  recognized, while ordinary shell arguments cannot select OMP status rules.

## 4. Complete launch/harness controls

- Reproduction: `OMPOptions` held only `Model`; the new-session UI displayed
  only the shared model input, so every other issue-requested flag required a
  raw command workaround.
- Root cause: `internal/session/tooloptions.go:323-360` and
  `internal/session/userconfig.go:2127-2148` modeled only the initial subset,
  and `internal/ui/newdialog.go` had no OMP options panel/wiring.
- Fix: `internal/session/tooloptions.go:323-450` models session mode/resume,
  no-session, model/models, smol/slow/plan roles, thoughts, approval,
  auto-approve, max-time, profile, and both imports. Configuration now supplies
  default profile and role models. `internal/ui/ompoptions.go:12-173` exposes
  every control and `internal/ui/home.go` persists the structured selection.
  Documentation was updated in `docs/tools/omp.md` and the agent-deck skill
  config reference.
- Red-without-fix: parent production plus the session option tests failed to
  compile on the absent fields (`SESSION_MUTATION_EXIT=1`); parent production
  plus the UI tests failed on absent `NewOMPOptionsPanel`, dialog field, and
  getter (`UI_MUTATION_EXIT=1`). Both are API-contract failures caused by the
  reverted production hunks.
- Preserved invariant: default managed sessions remain `--continue` and
  instance-scoped; explicit `new`, `resume`, and `no-session` choices alter the
  lifecycle flag without dropping shell quoting or model/config defaults.

## 5. Web SkillsPane surface

- Reproduction: no test executed or pinned the shipped JavaScript OMP support
  branch and its attach/detach actions.
- Root cause: `internal/web/static/app/panes/SkillsPane.js:16-74` was changed
  without a Web-package regression test.
- Fix: `internal/web/omp_skills_surface_test.go` reads the actual embedded JS
  asset and pins the OMP support entry, supported rendering branch, and POST /
  DELETE attach/detach actions.
- Red-without-fix: after removing only `'omp'` from the embedded SkillsPane set
  in `/tmp/r3-2054/mutation`, the test failed `embedded SkillsPane missing
  "'omp'"` (`WEB_MUTATION_EXIT=1`).
- Preserved invariant: unsupported tools still take the explicit unsupported
  rendering branch; existing attach/detach endpoint behavior is unchanged.

## 6. Remote parity

- Reproduction: the remote/sandbox `CanForkOMP` branch returned true without a
  lifecycle/fork assertion proving that target-side validation and options were
  retained.
- Root cause: `internal/session/omp.go:155-167` intentionally defers JSONL
  validation to the target command, but no remote-shaped fork test covered the
  resulting command.
- Fix: `TestOMPForkCarriesModelAndHarnessOptions` uses an OMP instance with
  `SSHHost` set, proves it reaches native fork creation without local filesystem
  inspection, and asserts target-side `$HOME` session paths, `--fork`, model,
  model cycle, profile, and persisted child options.
- Red-without-fix: the isolated `omp.go` mutation failed the exact remote fork
  assertions described in finding 2 (`FORK_MUTATION_EXIT=1`).
- Preserved invariant: local forks remain fail-closed when no JSONL exists;
  remote/sandbox forks retain runtime target-side validation because the local
  process cannot safely inspect the target filesystem.

## 7. GitHub intake metadata

- Reproduction: the PR body used legacy headings and lacked the required intake
  checklist and checked AI-disclosure choice.
- Root cause: PR metadata had not been migrated to the current repository
  template.
- Fix/proof: `gh pr edit 2054` replaced the body with all required headings,
  checklist, checked `AI-assisted`, named model, and gate marker; `gh pr view`
  readback confirmed the exact persisted body.
- Red-without-fix: metadata is not executable source and has no repository test;
  the before/after `gh pr view` readbacks are the direct proof. This is the one
  finding for which a code mutation test is inapplicable.
- Preserved invariant: the body still closes #1977 and retains concrete build,
  targeted-test, and mutation evidence; unchecked items were not falsely marked
  complete.

## Verification summary

- `go build ./... && go vet ./...`: PASS in `golang:1.25`.
- Focused current-head tests in `internal/session`, `internal/tmux`,
  `internal/ui`, and `internal/web`: PASS.
- Pre-existing OMP builder/fork tests included in the focused session run: PASS.
- Mutation exits: session 1, tmux 1, UI 1, Web 1, isolated fork 1, all for the
  intended missing contract/assertions documented above.
- No CHANGELOG or workflow files changed. PR was not merged.
