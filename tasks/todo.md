# fix/messaging-20260918 — messaging spine audit fixes (P1-1..P3-1)

- [x] P1-1 hooks install absolute binary path; `hooks status` PATH shadow + version mismatch; idempotent rewrite of bare entries
- [x] P1-2 ValidateTranscriptPath accepts CLAUDE_CONFIG_DIR + account slot dirs + worker-scratch, EvalSymlinks both sides, fail closed
- [x] P1-3 drain + sweeps take the inbox flock (lock order: inbox flock → consumedTurnsMu → inboxWriteMu)
- [x] P1-4 drop conductor-liveness gate; child_removed log-only; `inbox drain` exit 4 only with --strict, print per-store counts
- [x] P2-1 sync install exports AGENTDECK_STOP_SYNC marker; async-installed hook never drains
- [x] P2-2 per-target send lock in performSend (tmux + socket), bounded wait, "target busy with another send" verdict
- [x] P3-1 _unowned excluded from TTL sweep (sweep locked via P1-3)
- [x] go build/vet on host; Docker suite; code-simplifier; live `hooks status` read-only proof
- [x] RESULTS.md, PR-BODY.md (sanitized), commits with Claude-Session trailer

## Round 2 (review HOLD 7a5ae428)

- [ ] P1-A stable hook path (invoked symlink, never a Cellar dir) + self-heal on daemon start and `hooks status` (dangling / version-mismatched entry rewritten, idempotent, atomic, logged once); tests: Cellar-style upgrade heals, stable symlink chosen
- [ ] P1-B marker-less Stop entry keeps draining; only an explicit non-sync marker disables; unpinned check requires the marker so the heal / TUI path adds it; tests
- [ ] P2-C transcript roots include every [groups.*.claude] and [conductors.*.claude] config_dir; conductor fixture test
- [ ] P2-D wake-nudge idle gate re-probes status (hook fast path, pane fallback) under the daemon's probe budget; test
- [ ] P3 nudge subprocess timeout covers the send lock wait (constants tied); test
- [ ] go build/vet host; Docker suite; code-simplifier; live `hooks status` read-only
- [ ] RESULTS.md, PR-BODY.md, commits with Claude-Session trailer
