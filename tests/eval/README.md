# Behavioral Evaluator Harness

A second test layer that catches regressions where a Go unit test passes but
the user sees the wrong thing. Motivated by RFC
[`docs/rfc/EVALUATOR_HARNESS.md`](../../docs/rfc/EVALUATOR_HARNESS.md) (issue
#37) and three shipped-but-unit-test-invisible bugs:

1. **v1.7.35 CLI disclosure buffering** — `strings.Builder` hid the feedback
   disclosure until after `handleFeedback` returned. Unit tests also used
   `strings.Builder`, so the bug never showed up.
2. **v1.7.37 TUI disclosure missing** — the TUI jumped from `stepComment` to
   `stepSent` with no disclosure step, posting silently on Enter.
3. **#687 inject_status_line misdiagnosis** — unit tests asserted on struct
   fields and argv slices. Nobody asked what real tmux actually displayed.

## Layout

```
tests/eval/
├── harness/                # shared helpers
│   ├── sandbox.go          # scratch HOME, shim PATH, isolated tmux socket
│   ├── pty.go              # PTY spawn + ExpectOutput / ExpectOutputBefore
│   ├── gh_shim.go          # bash recorder + scripted exit for `gh`
│   └── tmux_shim.go        # wrapper that forces `-S <sock>` on every tmux call
├── feedback/
│   └── cli_test.go         # PTY-driven feedback CLI eval cases
├── session/
│   └── lifecycle_test.go   # real-tmux inject_status_line eval case
└── testdata/               # reserved for future fixture files
```

Eval cases that need package-internal access live alongside their subject —
for example `internal/ui/feedback_dialog_eval_test.go` drives the TUI
dialog directly because Go's internal-package rule blocks
`tests/eval/...` from importing `internal/ui`. These files still use the
`//go:build eval_smoke` tag and run under the same CI job.

## Running locally

```bash
# Smoke tier — what CI runs per-PR. Budget: ~30-60s.
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke \
  ./tests/eval/... ./internal/ui/...

# Full tier — runs at the release gate. Currently identical to smoke; will
# grow as eval_full cases are added.
GOTOOLCHAIN=go1.24.0 go test -tags 'eval_smoke eval_full' \
  ./tests/eval/... ./internal/ui/...

# Single case, verbose.
GOTOOLCHAIN=go1.24.0 go test -tags eval_smoke -v \
  -run TestEval_FeedbackCLI_DisclosureBeforeConsent ./tests/eval/feedback/...
```

`GOTOOLCHAIN=go1.24.0` is required — per `CLAUDE.md`, Go 1.25 breaks macOS
TUI rendering and the release CI pins 1.24.

## Writing a new eval case

The mental model: you are writing the test a user would run if they
themselves had to prove the feature works. Think PTY-observable behavior,
tmux state, files on disk, shim call records — **never** struct fields or
return values.

A minimal PTY case:

```go
//go:build eval_smoke

package feedback_test

import (
    "testing"
    "time"
    "github.com/asheshgoplani/agent-deck/tests/eval/harness"
)

func TestEval_MyNewCase(t *testing.T) {
    sb := harness.NewSandbox(t)
    sb.GhShim.ScriptSuccess()

    p := sb.Spawn("feedback")
    defer p.Close()

    p.ExpectOutput("Rating (1-5", 3*time.Second)
    p.Send("5\n")
    // ...
    p.ExpectExit(0, 5*time.Second)
}
```

A minimal tmux case:

```go
//go:build eval_smoke

package session_test

func TestEval_MyTmuxCase(t *testing.T) {
    sb := harness.NewSandbox(t)
    sb.InstallTmuxShim(t)
    // exec the agent-deck binary via sb.BinPath with sb.Env();
    // then query real tmux via sb.Tmux("display-message", "-p", "-t", name, fmt)
}
```

### Checklist for new cases

1. **TDD first.** Sabotage the product code to reintroduce the target bug,
   run the new test, watch it fail, restore the fix, watch it pass. If the
   test passes with the bug reintroduced, the test is not doing its job.
2. **Tag it.** `//go:build eval_smoke` at the very top of the file. No
   untagged eval files — they'd run under `go test ./...` and break local
   dev loops.
3. **Hermetic.** No live network. Use `sb.GhShim` for `gh`, install a tmux
   shim if you touch tmux. No reliance on `$HOME` state.
4. **Cheap.** Smoke-tier cases should take <2s each. If a case needs more,
   tag it `eval_full` instead (releases only).
5. **Useful failure messages.** When the test fails, the diagnostic should
   tell a future hotfix author what regressed. "want X before Y" beats
   "got false, want true."

## CI vs local

| Aspect | Local | CI (`eval-smoke.yml`) | CI (`release.yml`) |
|---|---|---|---|
| Platform | your dev box | Ubuntu latest | Ubuntu latest |
| Trigger | manual | every PR (paths-filtered) | every tag push |
| Tags | you choose | `eval_smoke` | `eval_smoke eval_full` |
| Timeout | your patience | 3m per `go test` | 6m per `go test` |
| `-race` | opt-in | always on | always on |
| macOS | works | n/a | n/a |

**macOS.** The harness runs on macOS locally but is not covered by CI. Some
termenv probes and tmux semantics differ between kernels; if you hit a
macOS-only failure, treat it as a yellow flag and investigate before
shipping, but don't block the PR on it.

## Troubleshooting

**"No packages found" gopls warning.** The test file is hidden behind a
build tag your editor doesn't know about. Add `"buildFlags": ["-tags=eval_smoke"]`
to your gopls settings.

**PTY output is garbage like `\x1b]11;?\x1b\\\x1b[6n`.** termenv is probing
the pseudo-terminal. The sandbox already sets `TERM=dumb` and
`AGENTDECK_COLOR=none` to suppress this. If you still see probes, a newly
imported library is initializing termenv directly; find it and either gate
on env or skip the probe.

**Tmux tests leave sessions behind.** The sandbox's teardown runs
`kill-server` on the per-test socket. If a test fails between `session
start` and teardown and you find stale sessions, they are on the isolated
socket — they won't interfere with your real tmux and will be cleaned up
when the socket path (a `t.TempDir()` subdir) is removed.

**"tmux: no server running" inside a test.** The first tmux call against a
fresh per-sandbox socket spins up a server. If you get this error, the
binary is calling tmux before the shim has taken effect — verify
`sb.InstallTmuxShim(t)` is called before `sb.Spawn(...)`.

## The mandate

Per `CLAUDE.md`'s watcher and feedback mandates, new interactive flows
should ship with an eval case. If you're adding a prompt, a tmux status
change, a disclosure step, or any behavior the user sees but pure Go tests
can't reach, add a case here before merging.
