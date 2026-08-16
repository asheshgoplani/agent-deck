package session

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/procowner"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Ownership receipts (#1873).
//
// A wrapped session can lose its tmux pane while the process tree it launched
// survives, reparented to PID 1. The pane is gone, so nothing that walks a pane
// tree or scans tmux sessions can see the survivor — and the next legitimate
// restart happily spawns a second tree against the same instance, worktree and
// conversation.
//
// The fix is a receipt written AT SPAWN: the pid of the pane's initial process
// bound to its start identity, made durable before anything can die. Every
// later decision — may this restart be admitted? which processes may be
// signalled? — is answered by verifying that receipt, never by scanning for
// processes that look like ours.
//
// This file is the session-layer wiring; internal/procowner holds the contract
// and the platform verification.

// ownershipAttribution* bound the descendant-attribution pass that runs during
// the spawn window.
//
// Fast at first, then relaxed: a wrapper that forks a child and exits does so
// within milliseconds, and a descendant can only be attributed while its
// ancestry back to the verified leader is still intact. After the first
// seconds, attribution is just keeping up with a normally-behaving session, so
// the cheaper cadence is enough. The pass stops at the fast-death window, which
// is where "this spawn is still starting" ends.
const (
	ownershipAttributeFastTick  = 50 * time.Millisecond
	ownershipAttributeFastFor   = 3 * time.Second
	ownershipAttributeSlowTick  = 500 * time.Millisecond
	ownershipAttributeMaxWindow = spawnFastDeathWindow
)

// ownershipProber and ownershipSignaler are package variables so tests can
// drive the whole state machine without a real process table. Production never
// replaces them.
var (
	ownershipProber   procowner.Prober   = procowner.NewProber()
	ownershipSignaler procowner.Signaler = procowner.OSSignaler{}
)

// ownershipStores caches one Store per directory.
var ownershipStores sync.Map // dir -> *procowner.Store

// ownershipReceiptLock is the cross-process serialization for a receipt's whole
// load → check → write cycle.
//
// It delegates to AcquireConfigFileLock, the tree's ONE implementation of
// "serialize a read-modify-write over a file two processes can both touch"
// (in-process mutex keyed by path, plus an advisory flock on a sibling .lock).
// A second, private lock here would be worse than none: two cross-process locks
// that do not know about each other serialize nothing between them, and the
// same defect has reappeared in this repository every time a shared rule was
// copied instead of called.
func ownershipReceiptLock(path string) (func(), error) {
	lock, err := AcquireConfigFileLock(path)
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}

// ownershipDir returns <data>/runtime/ownership, mirroring spawnFailureDir's
// fallback so an unresolvable data dir degrades to a temp path instead of
// losing the receipt.
func ownershipDir() string {
	path, err := runtimeDataPath("ownership")
	if err != nil {
		return tempAgentDeckPath("runtime", "ownership")
	}
	return path
}

// ownershipStoreAt returns the shared store for a directory.
func ownershipStoreAt(dir string) *procowner.Store {
	if existing, ok := ownershipStores.Load(dir); ok {
		return existing.(*procowner.Store)
	}
	created := procowner.NewStore(dir, ownershipReceiptLock)
	actual, _ := ownershipStores.LoadOrStore(dir, created)
	return actual.(*procowner.Store)
}

// ownershipStore returns the store for the live data dir.
func ownershipStore() *procowner.Store { return ownershipStoreAt(ownershipDir()) }

// OwnedProcessRecoveryRequiredError is returned instead of spawning when an
// instance still owns processes that a replacement would duplicate, or when
// ownership cannot be verified at all.
//
// It is deliberately a distinct type from an ordinary spawn failure: nothing
// was launched, nothing was signalled, and the operator has a specific next
// step. Callers that render errors get the facts; callers that retry on spawn
// failure must not treat this as one.
type OwnedProcessRecoveryRequiredError struct {
	InstanceID string
	Action     string // "start" | "restart"
	Generation uint64
	Verdict    string
	Reason     string
	Survivors  []procowner.Member
	// Details explain, per recorded identity, why it could not be verified.
	// Without them "could not be verified" is a verdict with no evidence, and
	// the operator has nothing to act on.
	Details []string
}

// Error implements error.
func (e *OwnedProcessRecoveryRequiredError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s refused: this session still owns processes from an earlier spawn (%s)",
		e.Action, e.Reason)
	for _, m := range e.Survivors {
		fmt.Fprintf(&b, "\n  owned: %s", m.String())
	}
	for _, detail := range e.Details {
		fmt.Fprintf(&b, "\n  unverifiable: %s", detail)
	}
	fmt.Fprintf(&b, "\nNothing was started and nothing was signalled.")
	fmt.Fprintf(&b, "\nInspect: agent-deck session ownership inspect %s", e.InstanceID)
	fmt.Fprintf(&b, "\nReap and clear: agent-deck session ownership reconcile %s", e.InstanceID)
	return b.String()
}

// IsOwnedProcessRecoveryRequired reports whether err is the fail-closed
// ownership refusal.
func IsOwnedProcessRecoveryRequired(err error) bool {
	var target *OwnedProcessRecoveryRequiredError
	return errors.As(err, &target)
}

// OwnershipStatus is the operator-facing view of an instance's receipt.
type OwnershipStatus struct {
	InstanceID string
	Receipt    *procowner.Receipt
	Report     procowner.Report
	// Survivors are owned processes that are NOT part of the live pane tree —
	// the escaped trees this issue is about.
	Survivors []procowner.Member
	// PaneAttached reports whether the live pane's initial process is the
	// receipt's leader, i.e. the receipt describes the running session.
	PaneAttached bool
	// LoadErr carries a corrupt-receipt error, which is a fail-closed state
	// rather than an absence of ownership.
	LoadErr error
}

// Admissible reports whether a spawn may proceed.
func (s OwnershipStatus) Admissible() bool {
	if s.LoadErr != nil {
		return false
	}
	if s.Receipt == nil {
		return true
	}
	if s.Report.Verdict == procowner.VerdictUnknown {
		return false
	}
	return len(s.Survivors) == 0
}

// Reason renders why a spawn is or is not admissible.
func (s OwnershipStatus) Reason() string {
	switch {
	case s.LoadErr != nil:
		return "the ownership receipt could not be read: " + s.LoadErr.Error()
	case s.Receipt == nil:
		return "no ownership receipt"
	case s.Report.Verdict == procowner.VerdictUnknown:
		return s.Report.Reason
	case len(s.Survivors) > 0:
		return fmt.Sprintf("%d owned process(es) from generation %d are alive outside the session's pane",
			len(s.Survivors), s.Receipt.Generation)
	default:
		return s.Report.Reason
	}
}

// ownershipStatus verifies the instance's receipt and classifies what it finds.
//
// The survivor computation is the part that matters. An owned process that is
// still a descendant of the live pane leader is the RUNNING SESSION, not a
// leak: refusing a restart because a healthy Claude session has live MCP
// children would make the guard worse than the bug. A survivor is an owned
// process that the live pane cannot account for — which is exactly the escaped
// tree #1873 reports.
func (i *Instance) ownershipStatus() OwnershipStatus {
	status := OwnershipStatus{InstanceID: i.ID}
	receipt, err := ownershipStore().Load(i.ID)
	if err != nil {
		status.LoadErr = err
		return status
	}
	if receipt == nil {
		return status
	}
	status.Receipt = receipt
	status.Report = procowner.Verify(ownershipProber, receipt)

	owned := status.Report.Owned()
	if len(owned) == 0 {
		return status
	}

	attached := i.liveOwnedPaneSet(receipt)
	status.PaneAttached = len(attached) > 0
	for _, m := range owned {
		if attached[m.Key()] {
			continue
		}
		status.Survivors = append(status.Survivors, m)
	}
	return status
}

// liveOwnedPaneSet returns the identities that are accounted for by the live
// pane: the pane's initial process when it is the receipt's leader, plus its
// current descendants.
//
// It returns an empty set the moment anything does not line up — no pane, an
// indeterminate probe, a pane whose leader is not the recorded one. Every one
// of those means "the live pane does not account for the receipt", and an empty
// set makes every owned identity a survivor, which is the fail-closed
// direction.
func (i *Instance) liveOwnedPaneSet(receipt *procowner.Receipt) map[string]bool {
	if receipt == nil {
		return nil
	}
	if !i.receiptLeaderRunsALivePane(receipt) {
		return nil
	}
	if procowner.VerifyMember(ownershipProber, receipt.Leader).State != procowner.StateOwned {
		return nil
	}
	leaderInfo, err := ownershipProber.Inspect(receipt.Leader.PID)
	if err != nil {
		return nil
	}
	set := map[string]bool{receipt.Leader.Key(): true}
	descendants, err := ownershipProber.Descendants(leaderInfo)
	if err != nil {
		// The leader itself is accounted for; its descendants are not, so they
		// stay classified as survivors and the restart fails closed.
		return set
	}
	for _, info := range descendants {
		set[procowner.Member{PID: info.PID, StartID: info.StartID}.Key()] = true
	}
	return set
}

// receiptLeaderRunsALivePane reports whether the receipt's leader is the
// initial process of a live tmux pane.
//
// Two sources are consulted, and either is enough:
//
//   - the instance's current tmux session, which is the normal case;
//   - the tmux session recorded IN THE RECEIPT at spawn.
//
// The second is not redundancy for its own sake. An Instance's in-memory tmux
// name can drift away from the live session it started — a stale name with a
// live session behind it is a real, separately-tracked failure that restart is
// expected to heal by adoption. Judging attachment by the drifted name alone
// would classify that session's own pane process as an escaped survivor and
// refuse the very restart that fixes it. The name written into the receipt at
// spawn does not drift, so it answers "is this process still inside the tmux
// session we launched it into?" correctly even when the instance has lost track.
func (i *Instance) receiptLeaderRunsALivePane(receipt *procowner.Receipt) bool {
	if i.tmuxSession != nil {
		if panePID, err := i.tmuxSession.PanePID(); err == nil && panePID == receipt.Leader.PID {
			return true
		}
	}
	if receipt.TmuxName == "" {
		return false
	}
	panePID, err := tmux.PanePIDOfSession(receipt.TmuxSocket, receipt.TmuxName)
	return err == nil && panePID == receipt.Leader.PID
}

// guardOwnedProcessesBeforeSpawn is the fail-closed admission gate. It runs
// inside the instance spawn lock, before any state is mutated and before any
// tmux work, on both the start and the restart path.
//
// It never signals anything. A refusal leaves the receipt exactly as it was, so
// the operator can inspect it and decide.
func (i *Instance) guardOwnedProcessesBeforeSpawn(action string) error {
	status := i.ownershipStatus()
	if status.Admissible() {
		if status.Receipt != nil && status.Report.Verdict == procowner.VerdictClear {
			// Every recorded process is provably gone: retire the receipt so
			// the replacement spawn starts from a clean slate rather than
			// inheriting a stale claim.
			if err := ownershipStore().Clear(status.Receipt); err != nil {
				sessionLog.Warn("ownership_receipt_clear_failed",
					slog.String("instance_id", logging.SanitizeValue(i.ID)),
					slog.String("error", logging.SanitizeValue(err.Error())))
			}
		}
		return nil
	}

	refusal := &OwnedProcessRecoveryRequiredError{
		InstanceID: i.ID,
		Action:     action,
		Verdict:    string(status.Report.Verdict),
		Reason:     status.Reason(),
		Survivors:  status.Survivors,
	}
	if status.Receipt != nil {
		refusal.Generation = status.Receipt.Generation
	}
	for _, member := range status.Report.Members {
		if member.State == procowner.StateUnknown {
			refusal.Details = append(refusal.Details,
				fmt.Sprintf("%s — %s", member.Member.String(), member.Detail))
		}
	}
	if status.LoadErr != nil {
		refusal.Verdict = string(procowner.VerdictUnknown)
	}
	sessionLog.Error("owned_process_recovery_required",
		slog.String("instance_id", logging.SanitizeValue(i.ID)),
		slog.String("action", logging.SanitizeValue(action)),
		slog.String("verdict", logging.SanitizeValue(refusal.Verdict)),
		slog.String("reason", logging.SanitizeValue(refusal.Reason)),
		slog.Int("survivors", len(status.Survivors)))
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "ownership_recovery_required",
		Source:     "ownership_gate",
		Reason:     refusal.Reason,
	})
	return refusal
}

// commitOwnershipAfterRestart re-claims ownership once a restart has replaced
// the pane's process.
//
// It runs deferred from restart(), which is what makes it reach every one of
// that function's exits: the per-tool respawn-pane fast paths each return
// early, and a receipt that still named the process respawn-pane just killed
// would leave the replacement unowned. A restart that never got as far as a
// live pane changes nothing — an unsuccessful restart has taken no new
// ownership, and the old receipt is still the truth about what may be alive.
func (i *Instance) commitOwnershipAfterRestart(command string) {
	if i.tmuxSession == nil {
		return
	}
	panePID, err := i.tmuxSession.PanePID()
	if err != nil || panePID <= 0 {
		return
	}
	if existing, loadErr := ownershipStore().Load(i.ID); loadErr == nil && existing != nil &&
		existing.Leader.PID == panePID &&
		procowner.VerifyMember(ownershipProber, existing.Leader).State == procowner.StateOwned {
		// The pane still runs the process the receipt already names (a restart
		// that returned before replacing anything). Nothing to re-claim.
		return
	}
	gen, wake := i.newSpawnGenWatch()
	i.claimOwnershipAtSpawn(command, gen, wake)
}

// claimOwnershipAtSpawn records the receipt for a pane that has just been
// started, and starts the descendant-attribution pass.
//
// gen/wake are the caller's spawn generation and supersede channel — the same
// pair the fast-death watcher gets — so attribution stops the instant a newer
// spawn or a deliberate stop takes over, and so claiming a receipt never
// supersedes the watcher the caller started alongside it.
//
// Best-effort by construction: a spawn that cannot be given a receipt is still
// a spawn. What it must never do is record a claim it cannot substantiate, so
// every failure path here writes nothing at all — no receipt means no ownership
// and therefore no signal, which is the safe direction.
//
// One window remains open and cannot be closed from here: agent-deck dying
// between tmux starting the pane and this function committing the receipt. The
// process exists before the receipt can name it — that ordering is forced by
// the kernel, not chosen — so a crash inside those few milliseconds leaves a
// pane agent-deck does not know it owns. It is orders of magnitude narrower
// than the window this issue is about (the whole fast-start window, plus every
// restart after it), and it fails in the same direction as an unsupported
// platform: unowned, never mis-owned.
func (i *Instance) claimOwnershipAtSpawn(command string, gen uint64, wake <-chan struct{}) {
	if i.tmuxSession == nil {
		return
	}
	store := ownershipStore()
	panePID, err := i.tmuxSession.PanePID()
	if err != nil || panePID <= 0 {
		sessionLog.Warn("ownership_receipt_skipped_no_pane_pid",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("error", logging.SanitizeValue(errString(err))))
		return
	}
	// Choosing the generation and writing the receipt are ONE critical section.
	// Split apart, two spawns can read the same predecessor and both conclude
	// they are its successor — an atomic write of a decision made on stale
	// state is just a reliable way to persist the wrong answer.
	receipt, err := store.Commit(i.ID, func(existing *procowner.Receipt) (*procowner.Receipt, error) {
		var generation uint64 = 1
		if existing != nil {
			generation = existing.Generation + 1
		}
		claimed, claimErr := procowner.Claim(ownershipProber, procowner.ClaimInput{
			InstanceID: i.ID,
			Generation: generation,
			PanePID:    panePID,
			TmuxName:   i.tmuxSession.Name,
			TmuxSocket: i.TmuxSocketName,
			Command:    redactEnvValues(command),
		})
		if claimErr != nil {
			// Unsupported platform, or a pane process that exited between the
			// tmux start and this probe. Either way: no claim, and the abort
			// leaves whatever was on disk untouched.
			return nil, claimErr
		}
		// Attribute once inside the same critical section so a wrapper that
		// forks and exits before the first tick is still caught, and so the
		// receipt reaches disk complete rather than in two writes.
		if _, attrErr := procowner.Attribute(ownershipProber, claimed, nil); attrErr != nil {
			sessionLog.Debug("ownership_attribution_initial_skipped",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("reason", logging.SanitizeValue(attrErr.Error())))
		}
		return claimed, nil
	})
	if err != nil {
		sessionLog.Info("ownership_receipt_not_claimed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("reason", logging.SanitizeValue(err.Error())))
		return
	}
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "ownership_claimed",
		Source:     "spawn",
		Reason:     fmt.Sprintf("generation %d, leader pid %d", receipt.Generation, receipt.Leader.PID),
	})

	// The store, the prober and the logger are all resolved HERE, in the
	// caller, and passed by value — the same discipline watchForFastDeath
	// follows and for the same reason. This goroutine is never joined, so it
	// can still be running after the test that started it has finished and
	// swapped those package variables out from under it. Reading them from the
	// goroutine is a data race the race detector will (and did) catch.
	go i.attributeOwnedTree(receipt, store, ownershipProber, sessionLog, gen, wake)
}

// attributeOwnedTree records the leader's descendants, each with its own start
// identity, for as long as the spawn window lasts.
//
// This is attribution, not discovery: it only ever walks DOWN from a leader
// that still verifies against the receipt, and it stops the moment that leader
// stops being ours. Nothing here can add a process that was not, at the instant
// of a single stat read, a live descendant of a process this spawn owns.
func (i *Instance) attributeOwnedTree(owned *procowner.Receipt, store *procowner.Store, prober procowner.Prober, logger *slog.Logger, gen uint64, wake <-chan struct{}) {
	start := time.Now()
	deadline := start.Add(ownershipAttributeMaxWindow)
	for {
		tick := ownershipAttributeSlowTick
		if time.Since(start) < ownershipAttributeFastFor {
			tick = ownershipAttributeFastTick
		}
		timer := time.NewTimer(tick)
		select {
		case <-timer.C:
		case <-wake:
			// Superseded by a newer spawn or a deliberate stop: that spawn owns
			// the receipt now.
			timer.Stop()
			return
		}

		if i.spawnGen.Load() != gen {
			return
		}

		// Attribution runs INSIDE the store's critical section, against the
		// receipt that is actually on disk — never against a private copy
		// written back afterwards. A copy-and-write-back would erase whatever
		// another writer recorded in the meantime: a member added by a
		// concurrent pass, or the recovery_required state a teardown just set.
		if err := store.Update(i.ID, func(current *procowner.Receipt) error {
			// Cancellation, checked AFTER the lock has been won rather than
			// before we queued for it. A stop that lands while this call waits
			// on the lock has to disarm this write, not merely stop the next
			// tick — otherwise a cleared receipt is resurrected by work that
			// was already in flight when it was cleared.
			if i.spawnGen.Load() != gen {
				return procowner.ErrWindowClosed
			}
			// Still the receipt this pass belongs to? Same rule Commit and
			// Clear enforce, called rather than restated.
			if err := procowner.RequireGeneration(current, owned.Generation, owned.Leader); err != nil {
				return err
			}
			added, err := procowner.Attribute(prober, current, nil)
			if err != nil {
				// The leader is gone, reused or unreadable. Whatever it left
				// behind was either already attributed (and is still owned) or
				// was never attributable at all; either way this pass has
				// nothing more to contribute and must not start guessing.
				return err
			}
			if len(added) == 0 {
				return procowner.ErrNoChange
			}
			return nil
		}); err != nil {
			// Every outcome here ends this pass: the window is over (a newer
			// spawn owns the receipt, or it has been reconciled away), or the
			// leader stopped being ours. Neither is a reason to keep scanning.
			logger.Debug("ownership_attribution_stopped",
				slog.String("instance_id", logging.SanitizeValue(i.ID)),
				slog.String("reason", logging.SanitizeValue(err.Error())))
			return
		}

		if time.Now().After(deadline) {
			return
		}
	}
}

// teardownReapOptions bounds how long a stop may wait on the ownership reap.
//
// The whole reap is two phases, not one per process, so this is the total added
// latency and not a per-member cost. The non-blocking Kill() path (the TUI's)
// gets the tighter budget already used by the MCP child reap on the same
// teardown, because a stop that appears to hang is its own bug; KillAndWait's
// callers are short-lived CLI processes that exist precisely to see the
// teardown through, so they get the longer one.
func teardownReapOptions(sync bool) procowner.ReapOptions {
	if sync {
		return procowner.ReapOptions{TermGrace: 3 * time.Second, KillGrace: 2 * time.Second}
	}
	return procowner.ReapOptions{TermGrace: time.Second, KillGrace: time.Second}
}

// clearOwnershipAfterTeardown reaps whatever the receipt still owns and clears
// it. Called from the teardown path, where "the session is being stopped" is
// exactly the intent that authorises terminating its processes.
//
// Only identity-matched processes are signalled. Anything unverifiable is left
// alone and the receipt is KEPT, so the operator can still see it — a receipt
// silently dropped here would put us straight back to an invisible survivor.
func (i *Instance) clearOwnershipAfterTeardown(sync bool) {
	store := ownershipStore()
	receipt, err := store.Load(i.ID)
	if err != nil {
		sessionLog.Warn("ownership_receipt_unreadable_on_teardown",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("error", logging.SanitizeValue(err.Error())))
		return
	}
	if receipt == nil {
		return
	}
	report := procowner.Reap(ownershipProber, ownershipSignaler, receipt, teardownReapOptions(sync))
	if report.Verdict != procowner.VerdictClear {
		markOwnershipRecoveryRequired(store, receipt, report.Reason)
		sessionLog.Warn("ownership_teardown_incomplete",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("verdict", logging.SanitizeValue(string(report.Verdict))),
			slog.String("reason", logging.SanitizeValue(report.Reason)))
		return
	}
	if err := store.Clear(receipt); err != nil && !errors.Is(err, procowner.ErrGenerationConflict) {
		sessionLog.Warn("ownership_receipt_clear_failed",
			slog.String("instance_id", logging.SanitizeValue(i.ID)),
			slog.String("error", logging.SanitizeValue(err.Error())))
		return
	}
	if report.Signalled() > 0 {
		_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
			InstanceID: i.ID,
			Tool:       i.Tool,
			Action:     "ownership_reaped",
			Source:     "teardown",
			Reason:     report.Reason,
		})
	}
}

// markOwnershipRecoveryRequired records why a receipt could not be retired.
//
// An in-place update, not a write-back: annotating a receipt that something
// else has already cleared would recreate it, and annotating a private copy
// would erase members another writer added while this reap was running.
// Best-effort otherwise — failing to annotate must not lose the receipt itself.
func markOwnershipRecoveryRequired(store *procowner.Store, receipt *procowner.Receipt, reason string) {
	_ = store.Update(receipt.InstanceID, func(current *procowner.Receipt) error {
		if err := procowner.RequireGeneration(current, receipt.Generation, receipt.Leader); err != nil {
			return err
		}
		current.State = procowner.StateRecoveryRequired
		current.Note = reason
		current.UpdatedAt = time.Now().Unix()
		return nil
	})
}

// OwnershipStatus reports what this instance owns, for `session ownership
// inspect` and any other read-only surface. It signals nothing.
func (i *Instance) OwnershipStatus() OwnershipStatus { return i.ownershipStatus() }

// ReconcileOwnership terminates every process this instance's receipt owns —
// identity-checked, verified dead — and clears the receipt when it succeeds.
//
// This is the explicit recovery operation. It is the ONLY path that signals as
// a result of an operator asking for it rather than as part of a teardown, and
// it still refuses to touch anything whose identity does not match.
func (i *Instance) ReconcileOwnership() (procowner.ReapReport, error) {
	store := ownershipStore()
	receipt, err := store.Load(i.ID)
	if err != nil {
		return procowner.ReapReport{}, err
	}
	if receipt == nil {
		return procowner.ReapReport{
			Verdict: procowner.VerdictClear,
			Reason:  "no ownership receipt",
		}, nil
	}
	report := procowner.Reap(ownershipProber, ownershipSignaler, receipt, procowner.ReapOptions{})
	if report.Verdict != procowner.VerdictClear {
		markOwnershipRecoveryRequired(store, receipt, report.Reason)
		return report, nil
	}
	if err := store.Clear(receipt); err != nil {
		return report, err
	}
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "ownership_reconciled",
		Source:     "operator",
		Reason:     report.Reason,
	})
	return report, nil
}

// AbandonOwnership discards a receipt without signalling anything.
//
// It exists because a receipt can become permanently unverifiable — a reused
// pid, an unreadable /proc entry — and the alternative to an explicit operator
// decision would be a session that can never be restarted. It is destructive in
// exactly one sense, and the caller must say so to the user: agent-deck stops
// managing whatever that receipt named. It never broadens matching, never kills
// by name, and never guesses.
func (i *Instance) AbandonOwnership() error {
	if err := ownershipStore().ForceClear(i.ID); err != nil {
		return err
	}
	_ = WriteSessionIDLifecycleEvent(SessionIDLifecycleEvent{
		InstanceID: i.ID,
		Tool:       i.Tool,
		Action:     "ownership_abandoned",
		Source:     "operator",
		Reason:     "receipt discarded without signalling; agent-deck no longer manages any survivor",
	})
	return nil
}
