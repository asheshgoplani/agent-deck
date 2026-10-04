# Comms matrix

`tests/comms_matrix/run.sh all` runs the model-free acceptance rig for #2482.
It builds the `comms-base` stage of `sandbox/Dockerfile`, builds the checkout's
Go binary in Docker, and starts a second container as `r1` over SSH. No real
model, account credentials, host home, or live tmux socket is used.

The reusable `comms-matrix.yml` workflow runs four shards from `go-test.yml`.
It is advisory for the first week and does not feed the required full-test-suite
gate. Review promotion after one week of clean real-PR runs; promotion is manual,
not automatic. Each shard retains
JSON assertions, exact command receipts, raw stdin bytes, timestamped pane
input, hook payloads, daemon logs and source/image identities. The shell runner
removes only its own compose project, volume, network and tagged images.

## Reading the result

Each assertion carries an actual value, target value, and status:

* `PASS`: that assertion observed its target.
* `XFAIL`: an explicitly enumerated current-main value differs from its target.
  This is a product gap, never release acceptance. It automatically becomes
  `PASS` when the observable value reaches the target.
* `FAIL`: an unexpected value, command error, malformed receipt or failed
  fixture. The process exits nonzero.
* `BLOCKED`: an unestablished precondition. This also exits nonzero.

Expected failures are scoped to particular assertions. A missing executable,
SSH failure, invalid JSON, dead daemon, or unknown error cannot be accepted as
an expected failure. Before P2, the exact missing `msg` command permits reading
`events follow --bus comms --json` instead; missing `msg export` remains XFAIL.
The exact public no-ledger response means an empty observation, not successful
text production. Claude and Codex require a real text record even on P1.

The baseline was developed against `ad1738b8ffddbac5c0fc7eae4109ed429fc3931e`.
Hermes info currently types one exact legacy waiting nudge despite the requested
background scenario. That specific observed line is XFAIL; any extra or different
input fails. Approval no-Enter assertions retain no allowance. The observed legacy-counter
counts are two or three for Hermes and two to five for Cursor against exactly one
status ledger record; those specific counts remain XFAIL. Empty/missing ledger
records cannot use that allowance. The daemon stops before the final ledger and
counter snapshots so those reads share a fixed observation boundary. These values
are a measured timing-dependent baseline envelope, not correct counter behavior.
It is not a waiver to merge later ledger phases. Those PRs must identify their
relevant cells and require target PASS, or explicitly document a waiver.

## Coverage and limits

The core grid executes all seven harnesses, local and SSH-remote, urgent and
info. It checks text, tier, keys, byte cap, public inbox counters, raw pane input,
producer receipts, sender/text/hash/final-delivery ledger records, urgent wake
counts and signal-to-record latency, and next-prompt/native delivery record IDs.
The remote grid checks source records, import origin, key preservation,
idempotent pulls and latency fields. Read source receipts separately from local
imports: a correct remote producer does not prove the pull landed.

Claude and Codex payloads are contextualized from the versioned fixtures in
`cmd/agent-deck/testdata/comms/`. The other payloads are synthetic protocol
fixtures, not recorded model runs. Pi executes the installed production
extension. OpenCode exposes HTTP/SSE and retains HTTP receipts; current main's
notify daemon does not connect to its SSE stream, and this remains XFAIL.
A working fake HTTP server is not a working production consumer.

The eight failure probes cover daemon restart during a burst with historical
input, superseded/removed parents, unreachable remote isolation, an unlinked
open log, 65 turns against the 64-turn limit, blocked composer and approval
safety. Supersession's exact current plan-only response is XFAIL; other errors
fail. Removed-parent acknowledgement probes `_unowned` twice. The queue probe
observes five seconds of progress, verifies fewer than 18 attempts and no
input, then explicitly removes its disposable target and joins its workers.
It does **not** prove the production 30-minute queue expiry. The synchronous
bounded-defer probe separately proves the sender receives an error on timeout.
The remote failure fixture delays a real SSH export beyond its three-second
command timeout while another remote answers. The measured bound covers two
parallel public CLI requests, not the daemon scheduler across a whole fleet.

Supplemental scenarios and their explicit remaining coverage gaps are emitted
by `scenarios.py`. The broader issue also includes human delivery, full
cross-host sibling topologies and real model token/CPU targets. Do not infer
those from a green baseline workflow. The separate
[lab soak](comms-lab-soak.md) is opt-in and has not been run by this CI task.
