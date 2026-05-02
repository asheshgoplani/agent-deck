# S22 — Multi-Instance Profile Coexistence

| Field | Value |
|---|---|
| Status | DRAFT — design only, awaiting maintainer decision |
| Branch | `docs/s22-multi-instance-rfc` |
| Author scope | Forensic walk + design proposals; no code |
| Inputs | Feedback Hub #600 (@vluts, 1/5 on v1.7.68); same-day 1/5 from @dk-blackfuel; maintainer review 2026-04-23; tmux NULL-deref #737 |
| Companion mitigation | Commit `5f5218c` (v1.7.68) — `softKillProcess` SIGTERM+grace |
| Branch HEAD at time of writing | `b371d4a` (~v1.7.43); v1.7.68 mitigations cited from `main` history |

---

## 1. Problem statement

A user running two `agent-deck` processes against the same profile (same `~/.agent-deck/profiles/<name>/state.db`, same default tmux socket) observes a control-pipe cascade:

> Each instance opens its own control pipes to the same `agentdeck_*` sessions and cyclically kills "stale" clients of the others (`killed_stale_control_client` cascade), which tears pipes and can kill tmux sessions that then respawn with new hash suffixes. — @vluts, FH#600

The reporter further claimed a `.lock` file with a stale-PID escape hatch. **This RFC will show no such file exists**: the gatekeeping mechanism is the `instance_heartbeats` SQLite table plus a transactional `ElectPrimary` claim. The reporter's symptom is real; the proposed root cause is one layer off, and the corrected root cause materially changes the design space.

The same class of failure produced #737 (tmux NULL-deref under SIGKILL on macOS Homebrew tmux 3.6a). v1.7.68's softKill softens *crash probability* under cascade but does not stop the cascade itself.

---

## 2. Truth-table of current behaviour

All file:line references are against branch HEAD `b371d4a`. Where v1.7.68 changes a referenced primitive, the change is called out inline.

### 2.1 The "lock" that isn't

There is **no `.lock` file** on disk. A repo-wide search for `flock`, `LOCK_EX`, `O_EXCL` against profile or daemon code returns zero hits in `internal/profile/`, `internal/session/`, `internal/statedb/`, or `cmd/`:

```
$ grep -rn "\.lock\b\|flock\|LOCK_EX\|O_EXCL" internal/ cmd/ | grep -v _test.go
internal/watcher/layout.go:83:  f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
internal/git/git.go:101:        // Check for ending with .lock
[no daemon-lifecycle hits]
```

`internal/profile/detect.go` is 47 lines of profile-name resolution from `AGENTDECK_PROFILE` / `CLAUDE_CONFIG_DIR`. It does not acquire a lock.

The actual gatekeeping mechanism is the `instance_heartbeats` table:

```sql
-- internal/statedb/statedb.go:234-242
CREATE TABLE IF NOT EXISTS instance_heartbeats (
    pid        INTEGER PRIMARY KEY,
    started    INTEGER NOT NULL,
    heartbeat  INTEGER NOT NULL,
    is_primary INTEGER NOT NULL DEFAULT 0
);
```

`RegisterInstance` (`statedb.go:684-704`) does an `INSERT OR REPLACE` — it does **not** refuse to register a second live PID. `ElectPrimary` (`statedb.go:735-779`) is transactional but only claims the `is_primary` flag; it does not prevent secondaries from running:

```go
// statedb.go:744-778 (paraphrased)
// 1. Clear is_primary=1 rows where heartbeat < cutoff (stale).
// 2. SELECT pid FROM instance_heartbeats WHERE is_primary=1 AND heartbeat>=cutoff.
// 3. If found: return existingPID == s.pid.
// 4. Else: UPDATE ... SET is_primary=1 WHERE pid=s.pid; return true.
```

**Reading**: the schema column is named `is_primary`, not `is_only`. The codebase **already contracts for N>1 instances per profile**: one primary, zero-or-more secondaries. Heartbeats are written from `internal/ui/home.go:699` and `cmd/agent-deck/main.go:387` on registration; refreshed by the home-loop SQLite sync at `internal/ui/home.go:2873`.

> Maintainer-facing implication: any proposal that exits a secondary instance with an error reverses an existing schema contract. Any proposal that lets secondaries coexist must teach every cleanup primitive to honour that contract.

### 2.2 Tmux socket selection — global, not per-profile

A repo-wide search for the `-L` flag (tmux's socket-name override) against daemon code returns no occurrences:

```
$ grep -rn "socket_name\|SocketName\|tmux.*-L\b\|\"-L\"," internal/tmux/ internal/session/ cmd/ | grep -v _test.go
internal/tmux/tmux.go:1514:  for _, socketPath := range defaultTmuxSocketCandidates()
internal/tmux/tmux.go:1541:  func defaultTmuxSocketCandidates() []string
```

All `tmux` invocations run against the **default per-uid socket** (`~/.tmux-<uid>/default`, candidates assembled at `internal/tmux/tmux.go:1541-1569`). Concrete examples:

```go
// internal/tmux/tmux.go:922
tmuxArgs := []string{"new-session", "-d", "-s", s.Name, "-c", workDir}
// internal/tmux/tmux.go:1392
_ = exec.Command("tmux", "kill-session", "-t", name).Run()
// internal/tmux/tmux.go:2046
cmd := exec.Command("tmux", "kill-session", "-t", s.Name)
```

No `-L socket_name` is threaded. There is no `[tmux].socket_name` config key on this branch (despite issues #687 / #707 / #718 introducing socket-isolation phases — those landed on `main` after this branch's HEAD, see commit `d310112` "fix(tmux): socket isolation on attach + all pty.go subprocess paths"). What exists at HEAD is a per-installation socket-recovery path (`recoverFromStaleDefaultSocketIfNeeded`, line 1504); it does not give a per-profile socket.

> Maintainer-facing implication: two daemons on the same uid see the same socket regardless of profile, regardless of installation directory. Cross-profile cascades are possible *today* — not just same-profile. The blast radius of the cascade is the host, not the profile.

### 2.3 Control pipe lifecycle and the cascade primitive

The single load-bearing primitive is `killStaleControlClients` at `internal/tmux/pipemanager.go:433-467`:

```go
// pipemanager.go:426-467 (verbatim, lightly trimmed)
// killStaleControlClients kills control-mode clients attached to a session that
// are not owned by the current process. These accumulate when the TUI is killed
// without clean shutdown and restarted — the old `tmux -C attach-session`
// processes survive because they run in their own process group (#595).
func killStaleControlClients(sessionName string) {
    myPID := os.Getpid()
    out, err := exec.Command(
        "tmux", "list-clients", "-t", sessionName,
        "-F", "#{client_control_mode} #{client_pid}",
    ).Output()
    if err != nil {
        return
    }
    for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
        // ... parse "<control_mode> <pid>" ...
        if pid == myPID {
            continue // don't kill our own process
        }
        if proc, err := os.FindProcess(pid); err == nil {
            _ = proc.Kill()
            pipeLog.Debug("killed_stale_control_client", ...)
        }
    }
}
```

Critical observations:

1. **The "stale" predicate is `pid != myPID`.** Any other agent-deck instance attached to the same tmux session is by definition "stale" by this check — there is no consultation of `instance_heartbeats`, no liveness probe, no ownership marker.
2. **The kill is immediate `proc.Kill()` (SIGKILL)** at line 461. This is the call signature on this branch. Commit `5f5218c` (v1.7.68 / #737) on `main` softens this to SIGTERM + 500 ms grace + SIGKILL fallback (`softKillProcess`, `controlClientKillGrace = 500*time.Millisecond`). That softening reduces the probability of the macOS tmux 3.6a NULL-deref but does not change the targeting logic — peers are still killed.
3. **Cadence**: `killStaleControlClients` is called from `Connect()` at `pipemanager.go:87`, **on every pipe (re)connection attempt**. There is no cooldown. A reconnect flap on instance A trivially produces a reconnect flap on instance B, and vice versa.

### 2.4 Cleanup paths that assume sole ownership

Every `tmux kill-session` and `kill-server` site implicitly assumes the caller is the only agent-deck instance on the socket. Examples beyond §2.2:

- `internal/session/restart_sweep.go` shells to `tmux.KillSessionsWithEnvValue`, which iterates `tmux list-sessions` on the global socket and removes anything matching an env value. Two daemons, same `AGENTDECK_*` env stamping, one starts a sweep — the other's sessions go with it.
- `internal/tmux/pipemanager.go:114-127` (`Disconnect`) closes stdin on the control pipe and reaps the process group. If two PipeManager instances think they own the pipe, only one is right.
- Session removal flows that issue `tmux kill-session -t <name>` (e.g. `tmux.go:2046`) treat the session name as globally addressable. Hash-suffix respawn (described in @vluts's report) is the symptom: a session killed by peer-A is recreated by peer-B with a new disambiguating suffix, and the user's session list grows lossily.

### 2.5 SQLite + WAL behaviour

`state.db` opens with WAL and a 5 s busy timeout:

```go
// statedb.go:142
PRAGMA journal_mode=WAL
// statedb.go:148
PRAGMA busy_timeout=5000
// statedb.go:154
PRAGMA foreign_keys=ON
```

WAL allows concurrent readers and a single writer; `busy_timeout=5000` makes contending writers wait up to 5 seconds before returning `SQLITE_BUSY`. There is **no application-level mutex** beyond what SQLite provides. The `ElectPrimary` transaction (§2.1) is the closest thing to an inter-process critical section, and it doesn't gate any code paths outside primary election itself.

The DB is not the cascade trigger — it tolerates N writers fine — but it is also not preventing N daemons from arising. The "lock-like" behaviour the reporter expected lives only inside `ElectPrimary` and is scoped to a single boolean column.

### 2.6 Cross-OS behaviour

There is no `_linux.go` / `_darwin.go` split on the lock or pipe-cleanup paths. `os.FindProcess(pid)` followed by `proc.Kill()` is POSIX-uniform. The relevant OS-shaped difference is in the *failure mode* of the cascade target, not the cascade source:

- **Linux**: SIGKILL on a `tmux -C attach-session` client tears the FIFO pipe; the parent agent-deck retries. No tmux server crash typically.
- **macOS Homebrew tmux 3.6a**: SIGKILL during control-mode notify path triggers a NULL-deref (#737). The tmux *server* dies, taking every session with it. v1.7.68's SIGTERM+grace mitigates this specific crash.

> Maintainer-facing implication: macOS users see "all my sessions vanished and respawned with new hash suffixes" because the tmux server is being recycled mid-cascade. Linux users see "my pipes flap" but server typically survives. Same root cause, very different symptom.

### 2.7 The cascade, annotated

Two daemons D1 and D2 on the same uid, same profile:

| Step | Actor | Code path | Effect |
|---|---|---|---|
| 1 | D1 startup | `RegisterInstance` (`statedb.go:684`) | Row inserted; `ElectPrimary` claims `is_primary=1` |
| 2 | D2 startup | `RegisterInstance` | Second row inserted; `ElectPrimary` sees D1 alive, returns false; D2 runs as secondary |
| 3 | D1 connects to `agentdeck_foo` | `PipeManager.Connect()` calls `killStaleControlClients` (`pipemanager.go:87`) | No clients yet — no-op |
| 4 | D1 spawns control pipe | `tmux -C attach-session -t agentdeck_foo` | Client `pid_D1_ctrl` registered with tmux |
| 5 | D2 connects to `agentdeck_foo` | Same `Connect()` → `killStaleControlClients` | `tmux list-clients` returns `pid_D1_ctrl`; predicate `pid != myPID` is true; SIGKILL sent (line 461). On `main`/v1.7.68: SIGTERM+grace |
| 6 | D1's pipe dies | Reader on pipe sees EOF | D1's reconnect logic kicks in (`pipemanager.go:~200-420`) |
| 7 | D2 spawns its own pipe | Fresh control client `pid_D2_ctrl` | |
| 8 | D1 retries `Connect()` | `killStaleControlClients` again | Finds `pid_D2_ctrl`; predicate true; SIGKILL sent to D2's pipe |
| 9 | Cascade | Steps 5–8 repeat | Pipe-flap. On macOS+tmux-3.6a, SIGKILL trips the NULL-deref → tmux server dies → sessions respawn with new hash suffixes |

The cascade is **deterministic** — not racey. Two daemons + one shared session is sufficient. The reporter's `.lock` framing predicted a startup race; the actual mechanism is a steady-state cleanup-primitive contract violation.

---

## 3. Reframing — what the code actually contracts for

| Surface | Contract today |
|---|---|
| `instance_heartbeats` schema | Multi-instance: one primary, N secondaries |
| `ElectPrimary` semantics | Coordinates primaryship, not exclusion |
| `state.db` (WAL) | N concurrent readers + serialised writers, by design |
| Tmux socket | Global per uid; **no isolation** |
| `killStaleControlClients` | "All non-self clients are leaked-from-my-prior-life" — single-instance assumption |
| `restart_sweep` / `kill-session` | "I own everything matching this name/env on this socket" — single-instance assumption |

The first three rows are multi-instance-aware. The last three are not. **The bug is the inconsistency** — not "we never thought about multi-instance," but "we thought about it in the persistence layer and forgot in the cleanup layer." That framing is what makes Proposal C tractable.

---

## 4. Proposal A — Hard lock + fast-exit

Treat multi-instance as undefined behaviour. Detect at startup, exit cleanly with a clear error.

### 4.1 Mechanism

1. New `internal/profile/lock.go`: acquires an advisory lock on `~/.agent-deck/profiles/<name>/agent-deck.lock` using `flock(LOCK_EX|LOCK_NB)` on Linux/BSD, `flock(2)` on macOS.
2. On `EWOULDBLOCK`: read the holder PID from the lock file; verify with `kill -0`. If alive, exit non-zero with a user-readable error pointing at the running PID. If dead (stale-PID), reclaim the lock atomically (recreate the file, write our PID, then `flock`).
3. Keep the lock held for the lifetime of the process. Release on clean shutdown via `defer` and on `SIGTERM` via signal handler.
4. Drop the `instance_heartbeats` `is_primary` column entirely (or keep as no-op). Rename `RegisterInstance` to `MarkPresent` for diagnostics only.
5. SIGKILL escape hatch: lock file's PID having no live process is the only way to reclaim. Document the failure mode.

### 4.2 Files / functions changed

- **New**: `internal/profile/lock.go`, `internal/profile/lock_unix.go`, `internal/profile/lock_other.go`.
- **Modified**: `cmd/agent-deck/main.go` (acquire lock before any DB or tmux call); `internal/statedb/statedb.go` (deprecate `ElectPrimary`/`is_primary`); `internal/ui/home.go:699` (no more secondary-mode); `internal/session/restart_sweep.go` (assumption now valid).
- **Removed**: nothing structural; secondary code paths become dead.

### 4.3 What breaks for users

- **TUI + parallel CLI on same profile**: today both work (CLI is a secondary). Under A, the second one errors out. A subset of CLI commands (`agent-deck session output`, `agent-deck mcp list`) become unusable when the TUI is running unless they bypass the lock.
- **Workaround needed**: split CLI into "lock-acquiring" (lifecycle: `start`, `stop`, `attach`) vs "read-only" (`list`, `output`) — read-only ops would open the DB read-only and skip the lock.
- **Multi-window users**: anyone who runs `agent-deck` in two terminals on purpose loses that capability. From maintainer review 2026-04-23 this is non-trivial — power users do this for "three projects on three sockets" workflows.

### 4.4 Migration

- Single release window, no schema migration needed (heartbeat table is additive-only).
- Document the change in `CHANGELOG.md` as a behaviour change requiring user awareness.
- Provide an env override `AGENTDECK_FORCE_INSTANCE=1` for emergency bypass (off by default).

### 4.5 Test surface

- `TestProfileLock_RejectsSecond` — second daemon on same profile errors.
- `TestProfileLock_StalePIDRecovered` — kill -9 first daemon, second can claim.
- `TestProfileLock_CleanShutdownReleases` — second can claim after first's `SIGTERM`.
- `TestProfileLock_ReadOnlyCLIBypass` — `agent-deck session output` works with TUI running.
- Cross-OS gate: existing `verify-session-persistence.sh` extended to run a "second-daemon-rejected" scenario.

Testable by current harness with moderate extension; no real-tmux multi-process scenario needed for the lock itself.

### 4.6 Operational invariant

> **At most one agent-deck process holds an exclusive lock on `~/.agent-deck/profiles/<name>/agent-deck.lock` at a time. All cleanup primitives MAY assume sole ownership of the tmux socket and the `instance_heartbeats` table. Stale-PID reclamation is the only mechanism by which a second process can take the lock; the prior process must be dead per `kill -0`.**

---

## 5. Proposal B — Per-instance isolation as a feature

Treat multi-instance as supported. Each instance gets its own SQLite + tmux socket + pipe namespace.

### 5.1 Mechanism

1. Introduce a config block:
   ```toml
   [multi_instance]
   mode = "auto"          # "auto" | "off" | "shared"
   instance_id_strategy = "ppid_hash"   # or "tmux_session", "uuid_persistent"
   ```
2. Derive an `instance_id` at startup — short stable hash of (uid, profile, working-tmux-session-id-if-any, ppid-bucket).
3. `[tmux].socket_name` becomes `agentdeck-<profile>-<instance_id>` by default (extending #687/#707/#718 socket isolation onto the per-instance axis).
4. SQLite path becomes `~/.agent-deck/profiles/<profile>/instances/<instance_id>/state.db`. Shared profile-level data (groups, recent sessions, cost events) lives in a separate `~/.agent-deck/profiles/<profile>/shared.db` opened read-mostly with the existing busy-timeout.
5. Pipe directory: per-instance under `~/.agent-deck/profiles/<profile>/instances/<instance_id>/pipes/`.
6. `mode = "off"` → behave like Proposal A (one instance, hard-lock).
7. `mode = "shared"` → legacy behaviour (current bug-prone path), kept only as escape hatch.

### 5.2 Files / functions changed

This is the largest delta. Touched surface:

- **New**: `internal/profile/instance.go` (instance ID resolver), `internal/profile/layout.go` (path computation), `internal/statedb/shared.go` (split shared vs instance DB).
- **Modified extensively**: `internal/statedb/statedb.go` (split into per-instance writes vs shared reads), `internal/tmux/tmux.go` (every `exec.Command("tmux", ...)` threads `-L socket`), `internal/tmux/pipemanager.go` (per-instance pipe dir, no cross-instance cleanup), `internal/session/*.go` (every storage path takes `Instance`), `cmd/agent-deck/main.go`, `cmd/agent-deck/session_cmd.go`, `cmd/agent-deck/watcher_cmd*.go`.
- **Schema migration**: groups, recent_sessions, cost_events relocate to `shared.db`. Heartbeat table relocates to instance DB or stays in shared with `instance_id` PK.
- **Config**: `[multi_instance]` block; `[tmux].socket_name` default rewritten in terms of `instance_id`.

Estimated files touched: 25–40. Estimated diff: 1500–2500 lines (excluding tests).

### 5.3 What breaks vs becomes possible

**Becomes possible:**
- Three agent-deck windows on three projects, three sockets, no cascade.
- `agent-deck session output` from a script never collides with the TUI.
- Per-instance debug isolation — one daemon can be paused under `dlv` without freezing the others.

**Breaks / new failure modes:**
- "I started a session in window A, why doesn't it appear in window B?" — every shared-data invariant must be re-derived. `groups` and `recent_sessions` need to live in `shared.db`; a wrong split is a regression vector.
- Watcher framework (`internal/watcher/**`) currently writes to the profile state DB; it must be re-pointed at `shared.db`. Per `CLAUDE.md` watcher-framework mandate, any change here requires the watcher test suite to re-pass — non-trivial.
- Disk usage: N instance DBs instead of one. Mitigated by `instance_id` reuse on restart.
- The session-persistence eight-test mandate (`TestPersistence_*`) currently tests one DB per profile. The mandate forbids removing those tests; they need to be updated to assert on the instance-DB paths.

### 5.4 Migration

- v(N): land Proposal B behind `[multi_instance].mode = "off"` (default off). Both code paths shipped.
- v(N+1): default flips to `"auto"`. Doc warning in changelog.
- v(N+2): `"shared"` removed.
- One-time migrator on first launch: copy `state.db` to `instances/<auto_id>/state.db`, copy shared tables into `shared.db`. Keep original as `state.db.legacy` for rollback. Requires a new test in the persistence suite (`TestPersistence_LegacyMigration`).

### 5.5 Test surface

- New: `TestMultiInstance_TwoDaemonsCoexist` — two PIDs, two sockets, no cross-pipe-kill.
- New: `TestMultiInstance_SharedGroupsVisible` — group created in instance A visible in instance B.
- New: `TestMultiInstance_PerSocketTmuxIsolation` — `tmux -L sock_A list-sessions` does not see B's sessions.
- New: `TestMultiInstance_LegacyMigration` — `state.db` from v(N-1) opens cleanly under v(N).
- Extended: all eight `TestPersistence_*` re-parameterised to run against per-instance DB layout.
- Watcher mandate: `verify-watcher-framework.sh` must pass against `shared.db`.

This is the largest test surface of the three proposals. `verify-session-persistence.sh` would need re-architecting to spawn two daemons and assert on isolation.

### 5.6 Operational invariant

> **Every agent-deck process owns a unique tuple `(profile, instance_id)`. Cleanup primitives MAY assume sole ownership of `<profile>/instances/<instance_id>/*` and the tmux socket `agentdeck-<profile>-<instance_id>`. They MUST NOT assume ownership of `<profile>/shared.db` or any tmux session not under their socket.**

---

## 6. Proposal C — Peer-aware cleanup *(third path; not in original brief)*

The forensic walk reveals (§3) that the codebase already contracts for multi-instance coexistence in its persistence layer. The cascade is caused by one cleanup primitive (`killStaleControlClients`) violating that contract. Proposal C is the minimal surgical fix that restores the contract.

### 6.1 Mechanism

1. `killStaleControlClients` consults `instance_heartbeats` before killing. The "stale" predicate becomes:
   - PID equals `myPID` → not stale (existing rule).
   - PID has a fresh heartbeat row in `instance_heartbeats` (`heartbeat >= now - threshold`) → **not stale, this is a peer**.
   - PID has a stale or absent heartbeat row → still stale, kill as today.
2. Add a defensive secondary check via `/proc/<pid>/comm` on Linux or `ps -p <pid> -o comm=` on macOS: only kill if the process is `tmux: client` or similar control-client signature. Refuse to send signals to processes not matching the expected program name. Closes the failure mode where heartbeat is stale-by-clock-skew but the PID is alive and ours.
3. Cooldown: once a peer is seen on a session, mark the session "shared" for that PipeManager and skip `killStaleControlClients` on subsequent reconnects for `N` seconds. Prevents any residual flap.
4. **Same change applied to `restart_sweep.go`**: don't sweep tmux sessions whose env-stamp PID is alive in `instance_heartbeats`.
5. Tmux remains on the shared socket. SQLite remains shared. No schema migration.

### 6.2 Files / functions changed

- `internal/tmux/pipemanager.go:433-467` — replace stale predicate. ~30 lines added.
- `internal/tmux/pipemanager.go` — add per-session "shared mode" map and cooldown. ~40 lines.
- `internal/session/restart_sweep.go` — peer-aware filter. ~20 lines.
- New: `internal/statedb/peers.go` — `IsAlivePeer(pid int) bool` helper, cached. ~50 lines.
- No changes to `cmd/`, no schema migration, no config.

Estimated diff: 150–250 lines + tests. **Roughly 10× smaller than B; ~comparable to A.**

### 6.3 What breaks vs becomes possible

**Becomes possible:**
- N daemons on the same profile coexist without cascade.
- `agent-deck session output` from a script while the TUI runs — works as advertised.

**Tradeoffs:**
- Both daemons see all sessions on the shared socket. This is current behaviour (it is what makes the cross-cascade *possible* today); under C it becomes the *intended* behaviour. UI implication: each instance's session list shows the union of all instances' sessions. May or may not be desired — needs a UX call.
- Cleanup is "best-effort" rather than authoritative. A truly leaked control client from a crashed prior daemon is reaped only after its heartbeat expires (default 30 s). Acceptable; the `proc.Kill()` path remains for the not-a-peer case.
- Doesn't solve cross-profile cascade (§2.2) — two daemons on different profiles still share the tmux socket. Mitigated by adopting the socket-isolation work from #687/#707/#718 onto `main` (already in progress) but does not require it.
- Heartbeat stale-by-clock-skew false positives: secondary check (`/proc/<pid>/comm`) closes this. Implementation has a real-world precedent in `internal/git/git.go` PID checks.

### 6.4 Migration

None. Single PR, no behaviour change visible to users not running multiple instances. For users running multiple instances, the behaviour change is "the cascade stops happening" — visible as a bug fix, not a feature toggle.

### 6.5 Test surface

- New: `TestKillStaleControlClients_SkipsAlivePeer` — register peer PID in heartbeats, ensure kill is skipped.
- New: `TestKillStaleControlClients_StillKillsTrueLeaks` — register no peer; existing kill behaviour preserved.
- New: `TestKillStaleControlClients_ProcCommGuard` — fake heartbeat row for a non-tmux PID, ensure kill is refused.
- New (integration): `TestMultiInstance_NoControlPipeCascade_C` — spawn two daemons against a shared session, assert pipe count remains 2 over a 30 s window.
- Existing `TestKillStaleControlClients` and `TestPipeManager_ConnectCleansStaleClients` must remain green.

Testable end-to-end with current harness. `verify-session-persistence.sh` extends with one new scenario; no re-architecture.

### 6.6 Operational invariant

> **Cleanup primitives that target tmux clients or sessions MUST consult `instance_heartbeats` before sending signals. A PID with a fresh heartbeat is a peer agent-deck instance and MUST NOT be killed. The `is_primary` column governs which instance owns *write authority* over shared resources; secondaries have *read authority* and *self-management authority* but NEVER *peer-management authority*.**

This invariant is general — it applies to any future cleanup primitive added to the codebase.

---

## 7. Operational invariants doc proposal

Independent of which proposal is chosen, this RFC recommends adding `docs/AUTOMATION_INVARIANTS.md` with at minimum:

### Section A — Process invariants

- **I-1 (Lock or peer-aware)**: Every cleanup primitive that sends signals to PIDs must either (a) hold an exclusive profile lock that excludes other instances entirely, or (b) consult a peer-discovery mechanism and refuse to signal peers. The current code does neither.
- **I-2 (Tmux ownership)**: A code path that calls `tmux kill-session` or `tmux kill-server` must declare which scope it claims authority over (instance, profile, host) and that scope must be enforced by socket isolation, name prefixing, or peer-awareness.
- **I-3 (Stamp before signal)**: Any session created by agent-deck must have an env-stamp identifying the creating instance. Cleanup primitives that act on env-stamp matches must additionally pass the I-1 check.
- **I-4 (Heartbeat is authoritative)**: A PID with a fresh `instance_heartbeats` row is alive *for the purpose of cleanup decisions*. Crash detection uses heartbeat staleness, not signal-0 racing.

### Section B — Storage invariants

- **I-5 (Shared schema enumeration)**: Tables that span instances (e.g. `groups`, `recent_sessions`, `cost_events`) must be explicitly listed and tested. Adding a new "shared" table without updating this list is forbidden.
- **I-6 (Migration is one-way)**: Schema migrations that move data between scopes (instance ↔ shared) require a regression test asserting old DBs open cleanly post-upgrade.

The existing `CLAUDE.md` already lists "session persistence: mandatory test coverage" and "watcher framework: mandatory test coverage." The proposed `AUTOMATION_INVARIANTS.md` is the orthogonal axis — *behavioural* invariants that the cleanup paths assume.

---

## 8. Comparison matrix

| Axis | Proposal A: Hard lock | Proposal B: Isolation | Proposal C: Peer-aware cleanup |
|---|---|---|---|
| Code delta | ~300 lines + tests | ~1500–2500 lines + tests | ~150–250 lines + tests |
| Schema migration | None | One-time copy | None |
| Breaks existing user behaviour | Yes (parallel CLI) | No (default `auto`) | No |
| Adds capability | No | Yes (true multi-instance) | Partial (multi-instance without socket isolation) |
| Solves cascade | Yes (no peers exist) | Yes (no shared resources) | Yes (peers recognised) |
| Solves cross-profile cascade | No (different profiles still uncoordinated) | Yes (different sockets) | No (without separately landing socket isolation) |
| Solves macOS tmux 3.6a NULL-deref | Indirectly (no SIGKILLs against peers) | Indirectly | Directly (no SIGKILLs against peers) — independent of v1.7.68 softKill |
| Test re-architecture needed | Minor | Major (eight persistence tests + watcher) | Minor |
| Ships with v1.7.68's softKill | Compatible | Compatible | Compatible — softKill remains a defence-in-depth |
| Risk of regression | Low (small surface) | High (surface area + migration) | Low–medium (per-OS `comm` probe needs careful testing) |
| Reverses existing schema contract | Yes (`is_primary` becomes vestigial) | No | No (uses `is_primary` as designed) |

---

## 9. Recommendation

**Ship Proposal C now. Track Proposal B as a v2 follow-up. Do not ship Proposal A.**

Rationale:

1. **C is consistent with the existing architecture.** §3 shows the persistence layer already contracts for multi-instance. C makes the cleanup layer honour the contract that's already there. A reverses the contract; B extends the contract substantially. The smallest design that the existing code is asking for is C.

2. **C ships in one PR, with minimal risk.** ~150–250 lines, no schema migration, no config surface, no user-visible behaviour change for single-instance users. The change is testable by the existing harness with one new scenario in `verify-session-persistence.sh`.

3. **C composes with B if we later want it.** B's per-instance socket isolation is independently valuable (it solves the cross-profile cascade in §2.2). C does not block B; B subsumes C. If the maintainer later decides power-user multi-instance is a first-class feature, B can land on top without un-doing C.

4. **A is the wrong call.** A is the "we never thought about multi-instance" fix. The schema shows we did. A removes a capability (parallel TUI + CLI) that v1.7.68 users currently enjoy. The maintainer-review-2026-04-23 "automation-on-automation" warning is *about* power users running stacked instances; locking them out is a regression for that exact constituency.

5. **v1.7.68's softKill is orthogonal and should be kept.** Even with C, a true leaked control client from a crashed prior daemon still gets signalled. SIGTERM+grace remains correct defence-in-depth for the macOS tmux 3.6a bug. C narrows the *targets* of the signal; v1.7.68 softens the *signal* itself.

6. **Add `docs/AUTOMATION_INVARIANTS.md` regardless.** Whichever proposal lands, the invariants in §7 are the missing documentation. They turn "we hope cleanup is safe" into "we have named the assumption and have a test that proves it."

### Decision the maintainer is being asked to make

Pick one of:

- (a) **C-only** — ship peer-aware cleanup, defer B, accept that cross-profile cascade remains until socket isolation lands separately. *RFC author's recommendation.*
- (b) **C + socket-isolation rebase** — ship C, also rebase the #687/#707/#718 socket isolation work (already on `main`) to make per-uid cross-profile cascades impossible. Belt-and-braces.
- (c) **B** — accept the larger surface, ship per-instance isolation, treat multi-instance as a first-class feature.
- (d) **A** — explicit single-instance enforcement, accept the regression for parallel-CLI users.

The maintainer is the right person to weigh (a) vs (b) vs (c). (d) is on the menu only if the project's design philosophy is "multi-instance is an accident we've been tolerating," which the schema does not support.

---

## Appendix A — Citations index

| Claim | Reference |
|---|---|
| No `.lock` file in daemon code | grep `internal/`, `cmd/` for `flock`, `LOCK_EX`, `O_EXCL` |
| `instance_heartbeats` schema | `internal/statedb/statedb.go:234-242` |
| `RegisterInstance` insert-or-replace | `internal/statedb/statedb.go:684-704` |
| `ElectPrimary` transaction | `internal/statedb/statedb.go:735-779` |
| `UnregisterInstance` cleanup | `internal/statedb/statedb.go:707-711` |
| `CleanDeadInstances` timeout | `internal/statedb/statedb.go:713-718` |
| `killStaleControlClients` definition | `internal/tmux/pipemanager.go:426-467` |
| `pid != myPID` predicate | `internal/tmux/pipemanager.go:456-457` |
| Immediate `proc.Kill()` (SIGKILL) at HEAD | `internal/tmux/pipemanager.go:460-461` |
| `Connect()` → `killStaleControlClients` cadence | `internal/tmux/pipemanager.go:87` |
| Tmux `new-session` without `-L` | `internal/tmux/tmux.go:922` |
| Tmux `kill-session` calls without `-L` | `internal/tmux/tmux.go:1392, 2046` |
| Default socket candidates | `internal/tmux/tmux.go:1541-1569` |
| Stale-socket recovery (per-host, not per-profile) | `internal/tmux/tmux.go:1504-1539` |
| WAL + 5s busy timeout | `internal/statedb/statedb.go:142, 148` |
| Profile detection (no lock) | `internal/profile/detect.go:1-47` |
| Heartbeat write call sites | `cmd/agent-deck/main.go:387`, `internal/ui/home.go:699` |
| v1.7.68 softKill (on `main`, not HEAD) | commit `5f5218c` "fix(tmux): soften killStaleControlClients to SIGTERM+grace (#737)" |
| Socket isolation work (on `main`, not HEAD) | commits `37a82c0` (#687, phase 1), `d310112` (#687, phase 2), `cb95b6e` (#755) |

## Appendix B — Out-of-scope / future work

- Cross-profile cascade (§2.2) is a strict superset of the in-profile cascade. Proposal C does not solve it. Any of the in-flight socket-isolation work (#687, #707, #718, #755) on `main` does. The two should be reconciled regardless of which proposal is chosen here.
- Watcher framework interaction (§5.3) is out of scope for C. If B is chosen, the watcher mandate (`CLAUDE.md` "watcher framework: mandatory test coverage") forces a re-pass that this RFC does not design.
- The `[tmux].socket_name` config key referenced in the original brief does not exist on this branch. The brief presumed it from `main`. Any RFC follow-up that mentions it should specify which branch's `main` it refers to.
- v1.7.68's regressions (#742–#746, hot-fixed in v1.7.69) are not in scope.

## Appendix C — Anti-recommendation: things this RFC explicitly rejects

- **"Add a sleep before `killStaleControlClients`"** — does not address the targeting bug.
- **"Switch SIGKILL to SIGTERM unconditionally"** — already done in v1.7.68; does not stop the cascade.
- **"Detect macOS tmux 3.6a and refuse to run"** — the cascade exists on Linux too; the Mac crash is a symptom amplifier, not the cause.
- **"Use `flock` on `state.db` itself"** — SQLite already manages its own locking; layering POSIX flock on top breaks WAL semantics.
- **"Make every CLI subcommand a primary-aware no-op"** — pushes the contract violation to a different layer; doesn't fix it.

---

*End of RFC. Maintainer review requested. No code changes are proposed in this document; the artefact for review is the design choice between A, B, and C.*
