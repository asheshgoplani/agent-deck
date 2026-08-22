# Communication reliability evaluator

Run `python3 eval/comms/run.py`. It builds `agent-deck`, creates isolated,
real tmux-backed sessions through `agent-deck add`, enumerates all 360 cells,
drives the public send/restart/send-keys/inbox commands, captures the actual
pane, and rewrites `MATRIX.md`, `matrix.json`, and `matrix.html`.

The PATH shims named `claude`, `codex`, and `gemini` are copies of
`fake_harness.py`. Thus the product's built-in command classification, launch,
tmux, prompt gating, text/Enter submission, retry, restart, and mode
preconditions are exercised without model/network calls. The fake only echoes
unique nonces. It does **not** emulate vendor JSONL transcripts, hooks, dialogs,
or an SSH server. Consequently `--wait`/`--stream` attribution and remote-SSH
cells remain red unless the product can prove them from real available state;
they are never silently skipped. Dialog, busy, and draft states are induced at
the pane/in-process level. “Just restarted” exclusively uses the product's
`session restart --force`; this evaluator has no process-kill/restart harness.

`inbox-talkback` sends through the product delivery path and invokes the durable
inbox drain, then uses pane/log evidence for exact delivery. The evaluator does
not fabricate internal inbox records because doing so would bypass the product.

This is initially report-only: the runner exits successfully after writing
results even when cells are red. Inspect `matrix.json` for weekly counts.
