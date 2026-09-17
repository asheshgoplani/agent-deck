# fix/messaging-20260918 — messaging spine audit fixes (P1-1..P3-1)

- [ ] P1-1 hooks install absolute binary path; `hooks status` PATH shadow + version mismatch; idempotent rewrite of bare entries
- [ ] P1-2 ValidateTranscriptPath accepts CLAUDE_CONFIG_DIR + account slot dirs + worker-scratch, EvalSymlinks both sides, fail closed
- [ ] P1-3 drain + sweeps take the inbox flock (lock order: inbox flock → consumedTurnsMu → inboxWriteMu)
- [ ] P1-4 drop conductor-liveness gate; child_removed log-only; `inbox drain` exit 4 only with --strict, print per-store counts
- [ ] P2-1 sync install exports AGENTDECK_STOP_SYNC marker; async-installed hook never drains
- [ ] P2-2 per-target send lock in performSend (tmux + socket), bounded wait, "target busy with another send" verdict
- [ ] P3-1 _unowned excluded from TTL sweep (sweep locked via P1-3)
- [ ] go build/vet on host; Docker suite; code-simplifier; live `hooks status` read-only proof
- [ ] RESULTS.md, PR-BODY.md (sanitized), commits with Claude-Session trailer
