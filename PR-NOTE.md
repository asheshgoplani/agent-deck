Thanks for #2080 — the bounded-skip fix for the stale-pane starvation case is
solid and unchanged here.

Independent review held on one point: interactive-state detection still
inferred an open AskUserQuestion picker purely from a raw tmux pane-text
capture, when a real busy/interactive signal already exists — the same
fresh hook-driven status (`"running"`/`"starting"`) the send path treats as
authoritative after the queued-delivery fix in #2273. A picker's
`PreToolUse` event never advances to `Stop`/`PostToolUse` until the human
answers, so that hook status alone already tells you the pane is
interactive, without guessing from glyphs.

On top of your two commits I added two more, `carry/2080`:

- `session show --json` now always reports `hook_status` and
  `hook_status_fresh`, mirroring what `--defer-if-busy` already reads
  internally.
- The bridge's `_pane_blocks_automated_send` gates on that hook signal
  first when it's known (confirmed `"hook-busy-interactive"`); pane-text
  picker detection now runs only as a fallback when the hook is unknown,
  and that fallback verdict is reported with an `"unknown:"` prefix so it's
  never confused with confirmed evidence. The composer-unsent-draft check
  is unchanged (it's orthogonal to turn state).
- Added a failing-first test proving the guard blocks on a completely
  neutral pane when the hook says interactive, plus coverage for the
  unknown-hook fallback and backward compatibility with the old call shape.

Full root-cause writeup and test evidence in `RESULTS.md`. Your original
commits are untouched.
