package session

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/safeio"
)

// Auth-death resilience (2026-07-26 fleet-death hardening).
//
// THE FAILURE THIS FIXES. Every Claude session on a host shares one set of
// credentials whose refresh token rotates and is single-use. When that token
// goes bad, EVERY session's agent hits 401 and exits. agent-deck used to see
// only "the pane is gone" and report a bare `error` — indistinguishable from a
// crash. Two consequences, both observed live:
//
//  1. To a human (and to a conductor) a fleet-wide 401 looks like MASS DEATH.
//     Nothing said "your token is broken"; the fleet just decayed, session by
//     session, from 62 panes to 51.
//  2. Every automatic recovery path then piled on. A restart cannot fix a bad
//     credential, so each one died again — and each doomed boot ALSO raced the
//     rotating refresh token, actively making the outage worse for the sessions
//     that were still alive. The recovery machinery became the amplifier.
//
// THE FIX, in three parts:
//
//   - Name it. A death whose last live pane showed a credential banner is
//     recorded as an AUTH HOLD, durably, before the evidence is lost with the
//     pane. Status stays `error` (byte-stable), but the substate now reads
//     auth-401 for a DEAD session too, so the TUI/CLI/web/transition events all
//     say "auth", not "?".
//   - Hold it, don't flap it. A held session is skipped by the automatic boot
//     paths (bulk restart, reviver sweep, watchdog-driven CLI restart) with a
//     concrete remedy printed instead. Explicit human intent — the TUI's R key,
//     or `session restart <id> --force` — always boots.
//   - Cap the blast radius. Bulk boots run through a gate with a concurrency
//     cap, jittered stagger, and a circuit breaker that stops the sweep after a
//     few consecutive auth deaths (see auth_boot_gate.go).
//
// The hold clears itself the moment the session is observed healthy again; it is
// never on a timer, because "the token was fixed" is not something a clock can
// know.

// AuthHoldRecord is the durable sidecar recording that a session is held out of
// automatic boots because its agent could not authenticate. It is written the
// moment the condition is observed and read by other processes (the CLI is a
// fresh process every invocation, so in-memory state alone cannot gate it).
type AuthHoldRecord struct {
	InstanceID string `json:"instance_id"`
	Tool       string `json:"tool,omitempty"`
	// Reason distinguishes a session still showing the banner from one whose
	// process already exited on it. Stable strings — the TUI/CLI switch on them.
	Reason string `json:"reason"` // auth_banner_live | auth_death
	// Evidence is the retained pane tail showing the banner. For auth_death it is
	// the last snapshot taken while the pane still existed.
	Evidence string `json:"evidence,omitempty"`
	// Timestamp is when the condition was (re-)observed, unix seconds.
	Timestamp int64 `json:"ts"`
	// BootAttempts counts automatic boots that have landed back in auth-death
	// since the hold was armed. Purely diagnostic — the hold itself blocks after
	// the first one.
	BootAttempts int `json:"boot_attempts,omitempty"`
}

// Auth hold reasons.
const (
	// AuthHoldReasonLive means the pane is still alive and rendering the
	// credential banner. The agent is up but cannot make progress.
	AuthHoldReasonLive = "auth_banner_live"
	// AuthHoldReasonDeath means the process exited and the last thing its pane
	// showed was the credential banner. This is the case that used to read as an
	// anonymous crash and drive the restart storm.
	AuthHoldReasonDeath = "auth_death"
)

// authHoldDeathWindow bounds how long a live auth-banner observation can justify
// classifying a LATER death as an auth death. A session that showed a 401,
// recovered, and crashed hours later for an unrelated reason must not be held.
// Generous enough to cover a slow exit after the banner renders (the agent
// typically exits within seconds), tight enough that an unrelated later crash
// falls outside it.
const authHoldDeathWindow = 10 * time.Minute

// authHoldDir returns <data>/runtime/auth-hold, mirroring spawnFailureDir's
// fallback so a data-dir resolution failure degrades to temp rather than
// panicking a status sweep.
func authHoldDir() string {
	path, err := runtimeDataPath("auth-hold")
	if err != nil {
		return tempAgentDeckPath("runtime", "auth-hold")
	}
	return path
}

func authHoldRecordPath(instanceID string) string {
	return filepath.Join(authHoldDir(), instanceID+".json")
}

// writeAuthHoldRecord persists a record atomically. Best-effort: a write failure
// must never block or crash the status path that observed the condition.
func writeAuthHoldRecord(rec AuthHoldRecord) error {
	if rec.Timestamp == 0 {
		rec.Timestamp = time.Now().Unix()
	}
	path := authHoldRecordPath(rec.InstanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create auth-hold dir: %w", err)
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal auth-hold record: %w", err)
	}
	// SkipBackup: the record is transient and self-clearing, a .bak is noise.
	return safeio.SafeOverwrite(path, data, safeio.Options{Perm: 0o644, SkipBackup: true})
}

// readAuthHoldRecord loads the sidecar for an instance, or (nil, nil) when none
// exists.
func readAuthHoldRecord(instanceID string) (*AuthHoldRecord, error) {
	data, err := os.ReadFile(authHoldRecordPath(instanceID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rec AuthHoldRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func clearAuthHoldRecord(instanceID string) {
	_ = safeio.SafeRemove(authHoldRecordPath(instanceID), safeio.RemoveOptions{})
}

// AuthHold returns this session's auth-hold record, or nil when it is not held.
// Reads the durable sidecar, so it works in a fresh CLI process that has no
// in-memory history.
func (i *Instance) AuthHold() *AuthHoldRecord {
	rec, err := readAuthHoldRecord(i.ID)
	if err != nil {
		return nil
	}
	return rec
}

// IsAuthHeld reports whether automatic boot paths must skip this session because
// it cannot authenticate, and returns the human-readable remedy to print.
//
// Held only while the session is NOT healthy: once it is observed running/
// waiting/idle the hold is stale by definition (and UpdateStatus clears it), so
// a lingering sidecar can never strand a working session.
//
// Status is read under the lock: this is called from unlocked reporting paths
// (Substate, the CLI restart gate, the boot sweep) that run concurrently with
// the TUI's background status poller writing Status under i.mu.
func (i *Instance) IsAuthHeld() (bool, string) {
	if isHealthyForFreshnessGuard(i.GetStatusThreadSafe()) {
		return false, ""
	}
	rec := i.AuthHold()
	if rec == nil {
		return false, ""
	}
	return true, rec.Remedy()
}

// Remedy is the one-line instruction a human needs. Deliberately concrete: the
// 2026-07-26 outage was prolonged because nothing on any surface said which
// action would fix it.
func (r *AuthHoldRecord) Remedy() string {
	if r == nil {
		return ""
	}
	what := "the agent exited because it could not authenticate"
	if r.Reason == AuthHoldReasonLive {
		what = "the agent is up but cannot authenticate"
	}
	return fmt.Sprintf(
		"%s — automatic restarts are held because they cannot fix a credential and each one races the shared token. Run /login (or repair the credentials), then press R to restart.",
		what,
	)
}

// FormatForDisplay renders the record as a preview-pane / `session show` block.
func (r *AuthHoldRecord) FormatForDisplay() string {
	if r == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString("🔒 authentication required\n")
	switch r.Reason {
	case AuthHoldReasonDeath:
		b.WriteString("The agent exited on an authentication failure (401 / invalid credentials).\n")
	case AuthHoldReasonLive:
		b.WriteString("The agent is running but every request fails authentication.\n")
	default:
		b.WriteString("The agent could not authenticate.\n")
	}
	b.WriteString("\nAutomatic restarts are HELD for this session: a restart cannot fix a bad\n")
	b.WriteString("credential, and every doomed boot races the shared refresh token — which is\n")
	b.WriteString("what turns one expired token into a fleet-wide outage.\n")
	if r.BootAttempts > 0 {
		fmt.Fprintf(&b, "\nautomatic boots that died on auth since: %d\n", r.BootAttempts)
	}
	if strings.TrimSpace(r.Evidence) != "" {
		b.WriteString("\nlast output before the failure:\n")
		b.WriteString(strings.TrimRight(r.Evidence, "\n"))
		b.WriteString("\n")
	}
	b.WriteString("\nFix: run /login in a working session (or repair the credentials file),\n")
	b.WriteString("then press R here to restart. `session restart <id> --force` also overrides.\n")
	return b.String()
}

// noteAuthHold arms (or refreshes) the hold for this instance. Caller holds i.mu
// or is otherwise single-threaded over i; only the sidecar and the in-memory
// timestamp are touched.
//
// Idempotent and cheap on the steady state: when a hold with the same reason is
// already on disk, nothing is rewritten, so a status sweep that sees the same
// wedged session every second does not thrash the filesystem.
func (i *Instance) noteAuthHoldLocked(reason, evidence string) {
	now := time.Now()
	i.authFailureSeenAt = now

	if existing, err := readAuthHoldRecord(i.ID); err == nil && existing != nil && existing.Reason == reason {
		return
	}

	rec := AuthHoldRecord{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Reason:     reason,
		Evidence:   evidence,
		Timestamp:  now.Unix(),
	}
	if err := writeAuthHoldRecord(rec); err != nil {
		sessionLog.Warn("auth_hold_write_failed",
			slog.String("instance_id", i.ID),
			slog.String("error", err.Error()))
		return
	}
	sessionLog.Warn("auth_hold_armed",
		slog.String("instance_id", i.ID),
		slog.String("title", i.Title),
		slog.String("reason", reason),
		slog.String("remedy", "run /login or repair credentials, then restart manually"))
}

// clearAuthHoldLocked releases the hold. Called from the status path the moment
// the session is observed healthy — the only evidence that actually proves the
// credential works again.
//
// It removes the sidecar UNCONDITIONALLY the first time this process sees the
// session healthy, then short-circuits on the in-memory flags. The first pass
// cannot be skipped: a sidecar written by a previous process (a TUI that observed
// the 401 and was then killed) is invisible to this process's flags, and leaving
// it on disk would make a LATER ordinary crash look like an auth death and hold a
// session a restart would have fixed. One unlink of a usually-absent path per
// session per process is the whole cost; every subsequent healthy sample is free.
func (i *Instance) clearAuthHoldLocked() {
	if i.authHoldClearedOnce && !i.authHeld && i.authFailureSeenAt.IsZero() {
		return
	}
	i.authHoldClearedOnce = true
	i.authFailureSeenAt = time.Time{}
	i.authHeld = false
	i.authHoldCheckedAt = time.Time{}
	clearAuthHoldRecord(i.ID)
}

// authHoldRecheckInterval throttles the sidecar read the death path does while
// it does not yet know whether this session is held. Once held is known
// in-memory, no further reads happen at all.
const authHoldRecheckInterval = 5 * time.Second

// refreshAuthHoldOnDeathLocked runs on the pane-death branches of UpdateStatus.
// It decides whether this death is an authentication death and, if so, makes the
// hold visible both durably (sidecar) and in-memory (i.authHeld, which the
// render hot path reads).
//
// Three cases, in order:
//
//  1. Already known held in-memory → nothing to do (no I/O).
//  2. A sidecar exists (possibly written by ANOTHER process — the TUI observed
//     the banner, this CLI invocation did not) → adopt it, and promote a
//     live-banner hold to a death hold now that the pane is confirmed gone.
//  3. No sidecar → ask the evidence: did the pane show a credential banner
//     before it vanished (authDeathObservedLocked)?
//
// Caller holds i.mu.
func (i *Instance) refreshAuthHoldOnDeathLocked() {
	if i.authHeld {
		return
	}
	if !i.authHoldCheckedAt.IsZero() && time.Since(i.authHoldCheckedAt) < authHoldRecheckInterval {
		return
	}
	i.authHoldCheckedAt = time.Now()

	if rec := i.AuthHold(); rec != nil {
		if rec.Reason != AuthHoldReasonDeath {
			i.noteAuthHoldLocked(AuthHoldReasonDeath, rec.Evidence)
		}
		i.authHeld = true
		return
	}
	if i.authDeathObservedLocked() {
		i.authHeld = true
	}
}

// reconcileAuthHoldLocked folds one status sample into the auth-hold state. It
// is the single place UpdateStatus's tmux-derived path arms, promotes, or
// releases the hold, so the three transitions can be read together:
//
//   - tmux "error"    — the pane is alive and rendering a banner. Arm the hold
//     ONLY when this sample's banner is a credential failure; a dropped socket
//     ("socket connection closed") shares the auth-401 substate but IS
//     restart-recoverable and must stay outside the hold.
//   - tmux "inactive" — the pane is gone. Attribute the death to auth when the
//     last readable sample said credential failure (or a hold already exists).
//   - healthy         — the credential demonstrably works again; release.
//
// Caller holds i.mu.
func (i *Instance) reconcileAuthHoldLocked(tmuxStatus string) {
	switch {
	case tmuxStatus == "error":
		ts := i.tmuxSession
		if ts == nil {
			return
		}
		evidence, isAuth := ts.LastSampleAuthFailure()
		if !isAuth {
			return
		}
		i.noteAuthHoldLocked(AuthHoldReasonLive, evidence)
		i.authHeld = true
	case tmuxStatus == "inactive":
		i.refreshAuthHoldOnDeathLocked()
	case isHealthyForFreshnessGuard(i.Status):
		i.clearAuthHoldLocked()
	}
}

// releaseAuthHoldIfHealthyLocked drops the hold when the settled status is
// healthy. Used by the status fast paths (hooks, OpenCode SSE) which return
// before the tmux-derived reconciliation runs. Caller holds i.mu.
func (i *Instance) releaseAuthHoldIfHealthyLocked() {
	if isHealthyForFreshnessGuard(i.Status) {
		i.clearAuthHoldLocked()
	}
}

// AuthHeldCached reports the in-memory auth-hold flag WITHOUT touching the
// filesystem. For the TUI render hot path and any other per-frame caller; the
// status sweep keeps the flag fresh. Use IsAuthHeld for a decision that must be
// correct across processes (CLI gates).
func (i *Instance) AuthHeldCached() bool {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.authHeld
}

// AuthHoldSurvivedBoot reports whether this session is auth-held AND at least
// one automatic boot has already died on that hold. Such a session is beyond
// machine recovery: the restart was tried and the credential still failed.
func (i *Instance) AuthHoldSurvivedBoot() bool {
	rec := i.AuthHold()
	return rec != nil && rec.BootAttempts > 0
}

// RecordAuthBootFailure bumps the diagnostic boot counter on an existing hold.
// Called by the bulk-boot gate when a boot it allowed landed straight back in
// auth-death, so `session show` can report how much thrash the session absorbed.
func (i *Instance) RecordAuthBootFailure() {
	rec := i.AuthHold()
	if rec == nil {
		return
	}
	rec.BootAttempts++
	rec.Timestamp = time.Now().Unix()
	_ = writeAuthHoldRecord(*rec)
}

// authDeathObservedLocked decides whether a just-detected pane death should be
// attributed to authentication, and arms the hold when it should.
//
// Two independent sources of truth, either sufficient:
//
//  1. The tmux session retained an auth-banner snapshot from a live sample
//     (tmux.Session.LastAuthFailureContent) — the strongest evidence, since the
//     banner was read off the real pane before it disappeared.
//  2. A live-banner hold was armed within authHoldDeathWindow — covers a death
//     observed by a different code path (or after the tmux object was replaced)
//     while the credential failure was demonstrably current.
//
// Returns true when the death was attributed to auth. Caller holds i.mu.
func (i *Instance) authDeathObservedLocked() bool {
	if ts := i.tmuxSession; ts != nil {
		if evidence, ok := ts.LastSampleAuthFailure(); ok {
			i.noteAuthHoldLocked(AuthHoldReasonDeath, evidence)
			return true
		}
	}
	if !i.authFailureSeenAt.IsZero() && time.Since(i.authFailureSeenAt) < authHoldDeathWindow {
		evidence := ""
		if rec := i.AuthHold(); rec != nil {
			evidence = rec.Evidence
		}
		i.noteAuthHoldLocked(AuthHoldReasonDeath, evidence)
		return true
	}
	// No auth evidence: an ordinary death, which a restart CAN fix. Leave it
	// alone so the existing recovery paths keep working.
	return false
}
