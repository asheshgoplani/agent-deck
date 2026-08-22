# Round 2 verification receipts

The gauntlet was invoked from this branch with the required absolute scripts and
`PATH=/tmp/g23/wt/build:$PATH`, after building `build/agent-deck` from this
worktree.

- `final-focused-test-and-build.txt`: focused regression tests and branch build (PASS).
- `mutation-dispatcher-key-0.txt`: mutation of the real, previously unlisted `0`
  dispatcher route to `shift+r`; the invariant rejects both `shift+r` and `R`
  aliases (PASS as a test-of-test).
- `final-captures/`: final core capture artifacts.
- `final-verdicts/`: final core judge artifacts.
- `captures/` and `verdicts/`: recovery capture and judge artifacts.

Observed required lines from the generated receipts:

- Station 03 Preview: PASS at 160x48 and 100x30.
- Station 03 Main: PASS at 100x30; REJECT at 160x48.
- Station 03 Help: REJECT at both sizes.
- Station 02 recovery `missing-tmux-session`: PASS at 160x48 and 100x30.
- Station 02 generic `error-preview`: REJECT at both sizes because the harness's
  `p` input leaves the fixture cursor on the selected group.

The judge text incorrectly states that the TUI reference assigns `$` to error
filtering. The captured branch source does not: the reference assigns `&` to
error filtering and `$` exclusively to Cost Dashboard. Raw receipts are retained
without rewriting their verdicts.
