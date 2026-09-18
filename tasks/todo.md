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

- [x] P1-A stable hook path (invoked symlink, never a Cellar dir) + self-heal on daemon start and `hooks status` (dangling / version-mismatched entry rewritten, idempotent, atomic, logged once); tests: Cellar-style upgrade heals, stable symlink chosen
- [x] P1-B marker-less Stop entry keeps draining; only an explicit non-sync marker disables; unpinned check requires the marker so the heal / TUI path adds it; tests
- [x] P2-C transcript roots include every [groups.*.claude] and [conductors.*.claude] config_dir; conductor fixture test
- [x] P2-D wake-nudge idle gate re-probes status (hook fast path, pane fallback) under the daemon's probe budget; test
- [x] P3 nudge subprocess timeout covers the send lock wait (constants tied); test
- [x] go build/vet host; Docker suite; code-simplifier; live `hooks status` read-only (on a copy of settings.json)
- [x] RESULTS.md, PR-BODY.md, commits with Claude-Session trailer

## Round 3 (review HOLD b18124b1)

- [x] F1 lossless heal: order-preserving JSON round trip, only agent-deck entries touched, atomic write, one-time `settings.json.bak-agentdeck-<ts>`, no write when unchanged, malformed JSON → error, no write; tests (user hooks + timeout + prompt hooks + custom matchers survive; idempotent; malformed)
- [x] F2 pin only stable install paths (/opt/homebrew/bin, /usr/local/bin, ~/.local/bin, ...); dev build → "unpinnable dev build: hooks keep the bare command", never written; `hooks status` read-only; older binary never heals a newer pin; tests per path class
- [x] F3 handler enforces "async install never drains" at drain time (settings.json Stop entry form + marker); test
- [x] F4 CLI-installed (drifted but present) entries count as installed for the TUI prompt; drift repaired silently, no prompt; test
- [x] go build/vet host; Docker suite; code-simplifier; live `hooks status` mtime proof
- [x] RESULTS.md, PR-BODY.md (sanitized), commits with Claude-Session trailer
