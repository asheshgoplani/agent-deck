package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/send"
)

// The send leg every doorbell goes through. A Claude parent is woken by a
// typed line, as always. A pi parent enrolled as a ledger consumer is woken
// over its extension's socket (design PR_B_DESIGN.md A4, B, J): the frame is a
// doorbell, never the records, which ride the run's before_agent_start
// injection and are acknowledged only by the hook.

// piWakeLegTimeout bounds one socket wake: dial 1 s plus the 2 s reply window,
// with headroom for the typed fallback's own subprocess budget.
const piWakeLegTimeout = 10 * time.Second

// legTracker counts background wake work. Wait polls, so a wait that gives up
// leaves no goroutine behind.
type legTracker struct{ n atomic.Int64 }

func (t *legTracker) Add(delta int) { t.n.Add(int64(delta)) }
func (t *legTracker) Done()         { t.n.Add(-1) }

// Wait blocks until the counted work is done or timeout passes.
func (t *legTracker) Wait(timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for t.n.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
}

// strayLegs counts background wake work no notifier owns: a doorbell sent
// through the package-level sendWakeNudge, and a typed fallback dispatched by a
// bare wakeSend. A notifier counts its own in its wiring.
var strayLegs legTracker

// wakeLegBound is the one bound a leg and Flush share: the socket's own bound,
// then the typed send's (which can wait on the target's lock) run one after the
// other, and Flush waits for exactly that.
const wakeLegBound = piWakeLegTimeout + wakeNudgeSendTimeout

// wakeNudgeGoroutineGate runs first in the goroutine a pi doorbell spawns,
// before any lock is taken. A test blocks it to hold a doorbell that is
// dispatched and reserved but not yet rung.
var wakeNudgeGoroutineGate = func() {}

// wakeBudgetCheck evaluates the budget of a socket wake. It runs outside the
// consumer lock: it opens storage.
var wakeBudgetCheck = wakeBudgetFor

// wakeReservations marks the doorbells dispatched and not yet rung, keyed by
// the ledger that holds the parent's consumer state and the parent, so the
// ledger pass of the same poll does not ring a second one.
var wakeReservations sync.Map

func wakeReservationKey(dir, id string) string { return dir + "|" + id }

// reserveWake records, synchronously at dispatch, that a doorbell for parent is
// on its way.
func reserveWake(parent *Instance) {
	now := commsNow()
	// Entries past their debounce can never suppress anything again.
	wakeReservations.Range(func(k, v any) bool {
		if now.Sub(v.(time.Time)) >= commsWakeDebounce {
			wakeReservations.Delete(k)
		}
		return true
	})
	if en, ok := LoadCommsEnrollment(parent.ID); ok {
		wakeReservations.Store(wakeReservationKey(en.Dir, parent.ID), now)
	}
}

// wakeReserved reports whether a doorbell for the parent was reserved inside
// the debounce window; an expired reservation is deleted.
func wakeReserved(dir, id string, now time.Time) bool {
	key := wakeReservationKey(dir, id)
	at, ok := wakeReservations.Load(key)
	if !ok {
		return false
	}
	if now.Sub(at.(time.Time)) >= commsWakeDebounce {
		wakeReservations.Delete(key)
		return false
	}
	return true
}

// wakeOpts says how a leg dispatches its typed fallback: in the background,
// tracked in legs (the default; legs may be nil for strayLegs), or in the
// foreground for a one-shot CLI that exits as soon as it returns.
type wakeOpts struct {
	legs       *legTracker
	foreground bool
	// resolved says the route was decided at dispatch and socket is that
	// decision: the leg does not look at the enrolment again.
	resolved, socket bool
}

// maxDoorbellIDs bounds the inbox ids one doorbell frame carries.
const maxDoorbellIDs = 8

// piWakeSocketFor is where the extension of parent instanceID listens ("" means
// no socket). A seam, because a test's HOME is too long for a unix socket path.
var piWakeSocketFor = func(profile, instanceID string) string {
	path, err := piSocketFile(profile, instanceID)
	if err != nil {
		return ""
	}
	return path
}

// SetPiWakeSeamsForTest points the pi doorbell for parentID at socket and has
// the peer check accept the pane process tree panePIDs, for tests outside this
// package (a pane-less e2e rig with an in-process fake extension). It returns
// the restore; flush the daemon's legs before calling it.
func SetPiWakeSeamsForTest(parentID, socket string, panePIDs map[int]bool) func() {
	prevPath, prevPIDs := piWakeSocketFor, piWakePanePIDs
	piWakeSocketFor = func(profile, id string) string {
		if id == parentID {
			return socket
		}
		return prevPath(profile, id)
	}
	piWakePanePIDs = func(inst *Instance) (map[int]bool, error) {
		if inst != nil && inst.ID == parentID {
			return panePIDs, nil
		}
		return prevPIDs(inst)
	}
	return func() { piWakeSocketFor, piWakePanePIDs = prevPath, prevPIDs }
}

// piWakePanePIDs is the pane process tree the socket's peer must be in, and the
// error of reading it: a tree read that failed may have missed the peer, so it
// proves nothing about it however much it returned.
var piWakePanePIDs = func(inst *Instance) (map[int]bool, error) {
	pids, err := inst.PaneProcessTreePIDs()
	set := make(map[int]bool, len(pids))
	for _, pid := range pids {
		set[pid] = true
	}
	return set, err
}

// piWakePlan is a doorbell whose attempt Do #1 carried: rung after the lock is
// released.
type piWakePlan struct {
	nonce string
	line  string
	ids   []string
}

// wakeOutcome is what the leg did with one doorbell.
type wakeOutcome struct {
	Via        string // the wake record's transport: socket or tmux
	State      string // landed, queued or pushed for the socket; "" is a typed line
	Accepted   bool   // the transport took it: submitted, or the line was typed
	Refused    bool   // nothing was sent and the records go back to pending
	Permanent  bool   // a refusal that would repeat: wakes for these records stop
	Err        string // the extension's word, or why a write is unconfirmed
	AfterWrite bool   // a typed fallback that followed a written frame
}

// ridesPiSocket reports whether parent's doorbells go over a socket: a pi
// session the ledger serves, whose hook injects what the doorbell announces.
func ridesPiSocket(parent *Instance) bool {
	return parent != nil && parent.Tool == "pi" && commsDeliversTo(parent.ID)
}

// wakeBudgetFor is WakeBudget with the profile's sessions loaded from storage,
// so a group daily limit sums the target's siblings. Storage is opened only
// when budgets are enforced; otherwise WakeBudget answers without it. When the
// sessions cannot be loaded the group limit cannot be summed: that is warned and
// the group check is skipped, never run on the target alone.
func wakeBudgetFor(profile string, target *Instance) costs.BudgetResult {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return WakeBudget(profile, target, nil, nil)
	}
	if budgets, _ := CostBudgetConfig(cfg); !budgets.Enforced() {
		return WakeBudget(profile, target, nil, nil)
	}
	// A target without its group: the session and total limits still apply.
	ungrouped := &Instance{ID: target.ID}
	storage, err := NewStorageWithProfile(profile)
	if err != nil {
		commsLog.Warn("wake_budget_group_check_skipped", slog.String("parent", target.ID), slog.String("error", err.Error()))
		return WakeBudget(profile, ungrouped, nil, nil)
	}
	defer func() { _ = storage.Close() }()
	instances, _, err := storage.LoadWithGroups()
	known := false
	for _, inst := range instances {
		known = known || (inst != nil && inst.ID == target.ID)
	}
	if err != nil || !known {
		// Without the target in the registry the group's members are unknown.
		reason := "target not in the registry"
		if err != nil {
			reason = err.Error()
		}
		commsLog.Warn("wake_budget_group_check_skipped", slog.String("parent", target.ID), slog.String("reason", reason))
		return WakeBudget(profile, ungrouped, nil, storage)
	}
	return WakeBudget(profile, target, instances, storage)
}

// wakeSend rings one doorbell for parent from the inbox path: a pi parent goes
// over its socket, anything else is typed. nonce is the frame id (a ULID) and
// ids the record ids the frame carries.
func wakeSend(parent *Instance, profile, line, nonce string, ids []string) (wakeOutcome, error) {
	return wakeSendOpt(wakeOpts{}, parent, profile, line, nonce, ids)
}

func wakeSendOpt(o wakeOpts, parent *Instance, profile, line, nonce string, ids []string) (wakeOutcome, error) {
	if !o.resolved {
		o.resolved, o.socket = true, ridesPiSocket(parent)
	}
	if !o.socket {
		err := sendWakeNudgeNoWait(profile, parent.ID, line)
		return wakeOutcome{Via: "tmux", Accepted: err == nil}, err
	}
	if res := wakeBudgetCheck(profile, parent); res.Action == costs.BudgetActionStop {
		PublishBudgetExceeded(profile, parent.ID, "socket", res)
		return wakeOutcome{Refused: true}, nil
	}
	// Do #1 for a doorbell that carries no ledger record: the debounce, so the
	// ledger pass of the same poll does not ring a second one.
	if r, _, ok := openLedgerFor(parent.ID); ok {
		_, _ = r.Do(parent.ID, func(p comms.Pass) ([]events.Cursor, error) {
			p.File.LastWake = commsNow().UnixMilli()
			return nil, nil
		})
		_ = r.Close()
	}
	return piWakeLeg(o, "inbox", parent, profile, line, nonce, ids)
}

// wakeDispatch is the nudge senders' entry to the leg for a doorbell that must
// not sit on the producer's commit path: it resolves the route once, reserves a
// pi doorbell synchronously so a ledger pass of the same poll does not ring a
// second one, and rings in a goroutine counted in legs.
func wakeDispatch(legs *legTracker, parent *Instance, profile, message string) error {
	if parent == nil {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = wakeNudgeMessage
	}
	rides := ridesPiSocket(parent)
	if rides {
		reserveWake(parent)
		legs.Add(1)
	}
	go func() {
		var err error
		if rides {
			defer legs.Done()
			wakeNudgeGoroutineGate()
			_, err = wakeSendOpt(wakeOpts{legs: legs, resolved: true, socket: true}, parent, profile, message, comms.NewID(time.Now()), inboxDoorbellIDs(parent.ID, message))
		} else {
			err = sendWakeNudgeNoWait(profile, parent.ID, message)
		}
		if err != nil {
			commsLog.Warn("wake_nudge_dispatch_failed", slog.String("parent", parent.ID), slog.String("error", err.Error()))
		}
	}()
	return nil
}

// wakeNudgeRoute is the synchronous entry a one-shot CLI drain uses: the typed
// fallback runs in the foreground too, as the process exits when this returns.
func wakeNudgeRoute(parent *Instance, profile, message string) error {
	if !ridesPiSocket(parent) {
		return sendWakeNudgeNoWait(profile, parent.ID, message)
	}
	_, err := wakeSendOpt(wakeOpts{foreground: true}, parent, profile, message, comms.NewID(time.Now()), inboxDoorbellIDs(parent.ID, message))
	return err
}

// inboxDoorbellIDs are the ids an inbox doorbell names: the fingerprints of the
// newest records waiting in the parent's inbox, or, when it has none to name, a
// hash of the line so a repeated nudge is still recognised.
func inboxDoorbellIDs(parentID, message string) []string {
	var ids []string
	if pending, err := ReadInboxEvents(parentID); err == nil {
		if len(pending) > maxDoorbellIDs {
			pending = pending[len(pending)-maxDoorbellIDs:]
		}
		for _, ev := range pending {
			fp := ev.TurnFingerprint
			if fp == "" {
				fp = TurnFingerprint(ev)
			}
			ids = append(ids, fp)
		}
	}
	if len(ids) == 0 {
		sum := sha256.Sum256([]byte(message))
		ids = []string{"line-" + hex.EncodeToString(sum[:])[:12]}
	}
	return ids
}

// piWakeLeg rings the doorbell over the socket and decides, from what came
// back, what the records and the line become (design B, J). Nothing here holds
// the consumer lock. The wake record is spooled from here with the real
// outcome, except for a transient refusal, which leaves none.
//
// A pi whose extension answers is never typed into: pi has no draft guard, so a
// typed line can merge into and submit an operator's draft, and the records
// reach pi at its next run start anyway. A pi launched with the socket exported
// has such an extension, so it is never typed into, whatever the connect says.
// Only a pi launched without it (too old, no extension, a path too long, socket
// off, or one an older binary launched) is typed a doorbell, as pi has always
// been woken, and only when nobody answers (no socket configured or no file, or
// a file nobody answers on). Any other unavailable (permissions, a peer the pane
// tree does not hold, a dial that failed for another reason) may hide a live pi
// and types nothing.
func piWakeLeg(o wakeOpts, origin string, parent *Instance, profile, line, nonce string, ids []string) (wakeOutcome, error) {
	ctx, cancel := context.WithTimeout(context.Background(), piWakeLegTimeout)
	defer cancel()
	panes, treeErr := piWakePanePIDs(parent)
	res, err := send.SendPiWake(ctx, send.PiWakeTarget{Path: piWakeSocketFor(profile, parent.ID)}, nonce, line, ids,
		send.PiWakeOptions{PanePIDs: panes})
	out := wakeOutcome{Via: "socket"}
	var unavailable *send.Unavailable
	var refused *send.PiWakeRefused
	switch {
	case err == nil:
		switch res.State {
		case send.PiWakeSubmitted:
			out.State, out.Accepted = comms.StateLanded, true
		case send.PiWakeQueued:
			out.State = comms.StateQueued
		default:
			out.State = comms.StatePushed
		}
	case errors.As(err, &unavailable):
		switch {
		case unavailable.Absent && parent.PiSocketExported:
			// Launched with the socket: a v5 extension exists, and a missing file
			// or a refused connection is a rebind or reload window, not an absent
			// pi. Nothing is typed; the records stay pending and the next pass
			// rings again.
			commsLog.Debug("pi_wake_socket_down", slog.String("parent", parent.ID), slog.String("reason", string(unavailable.Reason)))
			return wakeOutcome{Refused: true}, nil
		case unavailable.Absent:
			// Launched without the socket (or by a binary that predates it) and
			// nobody answers: pi has always been woken by the typed line.
			return typedDoorbell(o, origin, parent, profile, line, nonce, ids)
		case unavailable.Reason == send.ReasonPeerNotInPaneTree && (treeErr != nil || len(panes) == 0):
			// The pane tree could not be read (or read empty), so the peer is
			// unvetted and a live pi may be there: nothing known, nothing typed;
			// the settle retries.
			out.State, out.Err = comms.StatePushed, "pane_tree_unreadable"
		case unavailable.Reason == send.ReasonPeerNotInPaneTree:
			// A complete, successfully read tree that lacks the peer: something
			// other than this pane's pi owns the socket.
			commsLog.Warn("pi_wake_refused_permanent", slog.String("parent", parent.ID), slog.String("reason", "peer_not_in_pane_tree"))
			out = wakeOutcome{Via: "socket", State: comms.StateFailed, Refused: true, Permanent: true, Err: "peer_not_in_pane_tree"}
		case unavailable.Reason == send.ReasonContextDone || unavailable.Reason == send.ReasonDialFailed:
			// A cancel or a dial that failed for a reason that proves nothing: the
			// records stay pending and the next pass tries again.
			commsLog.Debug("pi_wake_unconfirmed", slog.String("parent", parent.ID), slog.String("reason", string(unavailable.Reason)))
			return wakeOutcome{Refused: true}, nil
		default:
			// A socket the wake will not use (permissions) may belong to a live pi.
			commsLog.Warn("pi_wake_refused_permanent", slog.String("parent", parent.ID), slog.String("reason", string(unavailable.Reason)))
			out = wakeOutcome{Via: "socket", State: comms.StateFailed, Refused: true, Permanent: true, Err: string(unavailable.Reason)}
		}
	case errors.As(err, &refused):
		if refused.AfterWrite && (refused.Reason == "busy" || refused.Reason == "duplicate") {
			// notSent is authoritative and the condition passes: pending, no record.
			commsLog.Debug("pi_wake_refused_transient", slog.String("parent", parent.ID), slog.String("reason", refused.Reason))
			return wakeOutcome{Refused: true}, nil
		}
		// Any other refusal would repeat every pass: the records stay pending
		// (they ride the next run start), socket wakes for them stop, and a
		// record names the reason.
		word := "pi_wake_refused_permanent"
		if !refused.AfterWrite {
			word = "pi_wake_refused_local"
		}
		commsLog.Warn(word, slog.String("parent", parent.ID), slog.String("reason", refused.Reason))
		out = wakeOutcome{Via: "socket", State: comms.StateFailed, Refused: true, Permanent: true, Err: refused.Reason, AfterWrite: refused.AfterWrite}
	default:
		// A write failure or a cancel after the write: nothing is known, so
		// nothing is typed and the 90 s settle retries once under a new nonce.
		out.State, out.Err = comms.StatePushed, "write_failed"
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			out.Err = "context_done"
		}
	}
	SpoolCommsWakeOutcome(parent.ID, origin, out.Via, out.State, line, nonce, out.Err, out.AfterWrite, ids)
	return out, nil
}

// typedDoorbell types the doorbell line into the pane when no extension is
// listening, as a Claude parent's wake is: dispatched in the background so it
// never holds a ledger pass (the send can wait on the target's lock), counted
// so a one-shot pass can wait for it, and recorded once it is sent. A one-shot
// CLI types in the foreground. The records count as accepted once dispatched.
func typedDoorbell(o wakeOpts, origin string, parent *Instance, profile, line, nonce string, ids []string) (wakeOutcome, error) {
	record := func() {
		SpoolCommsWakeOutcome(parent.ID, origin, "tmux", "", line, nonce, "", false, ids)
	}
	if o.foreground {
		if err := sendWakeNudgeNoWait(profile, parent.ID, line); err != nil {
			return wakeOutcome{Refused: true}, err
		}
		record()
		return wakeOutcome{Via: "tmux", Accepted: true}, nil
	}
	legs := o.legs
	if legs == nil {
		legs = &strayLegs
	}
	legs.Add(1)
	go func() {
		defer legs.Done()
		if err := sendWakeNudgeNoWait(profile, parent.ID, line); err != nil {
			commsLog.Warn("wake_nudge_dispatch_failed", slog.String("parent", parent.ID), slog.String("error", err.Error()))
			return
		}
		record()
	}()
	return wakeOutcome{Via: "tmux", Accepted: true}, nil
}

// ringPiWake rings the doorbell Do #1 carried and settles it (Do #2). The
// settle is compare-and-set on the nonce: an attempt a hook has since replaced
// or acknowledged is left to the hook.
func (d *TransitionDaemon) ringPiWake(r *comms.Reader, profile string, inst *Instance, plan *piWakePlan) {
	legs := &strayLegs
	if d.notifier != nil && d.notifier.wake != nil {
		legs = &d.notifier.wake.legs
	}
	out, err := piWakeLeg(wakeOpts{legs: legs}, "ledger", inst, profile, plan.line, plan.nonce, plan.ids)
	if err != nil {
		commsLog.Warn("comms_wake_send_failed", slog.String("parent", inst.ID), slog.String("error", err.Error()))
	}
	if _, err := r.Do(inst.ID, func(p comms.Pass) ([]events.Cursor, error) {
		switch {
		case out.Accepted:
			p.File.AcceptNonce(plan.nonce)
		case out.Refused:
			released := p.File.ReleaseNonce(plan.nonce)
			switch {
			case out.Permanent:
				// Stop socket wakes for these records: they ride the next run start.
				for i := 0; i < commsMaxWakeTries; i++ {
					p.File.NoteWakeFailed(released)
				}
			case len(released) > 0 && p.File.AutoWakes > 0:
				p.File.AutoWakes-- // a transient refusal never consumes the automatic wake cap
			}
		}
		return nil, nil
	}); err != nil {
		commsLog.Warn("comms_wake_settle_failed", slog.String("parent", inst.ID), slog.String("error", err.Error()))
	}
}

// StoppedSocketWake is a pi parent whose socket wakes a permanent refusal has
// stopped, and the reason the extension (or the peer check) gave.
type StoppedSocketWake struct {
	Parent string
	Reason string
}

// PiSocketWakesStopped lists the enrolled pi parents whose records have used up
// their wake tries and whose last socket wake was recorded as failed, so
// `pi-hooks status` can say why no doorbell rings for them (the records still
// arrive with the parent's next run start).
func PiSocketWakesStopped() []StoppedSocketWake {
	entries, err := os.ReadDir(commsEnrollmentDir())
	if err != nil {
		return nil
	}
	var out []StoppedSocketWake
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".json")
		en, ok := LoadCommsEnrollment(id)
		if !ok || en.Tool != "pi" || en.Draining {
			continue
		}
		f, found, err := comms.ReadConsumer(en.Dir, id)
		if err != nil || !found {
			continue
		}
		stopped := false
		for _, tries := range f.WakeTries {
			stopped = stopped || tries >= commsMaxWakeTries
		}
		if !stopped {
			continue
		}
		if reason := lastFailedSocketWake(en.Dir, id); reason != "" {
			out = append(out, StoppedSocketWake{Parent: id, Reason: reason})
		}
	}
	return out
}

// lastFailedSocketWake is the reason on the newest failed socket wake record
// addressed to parent, "" when there is none.
func lastFailedSocketWake(dir, parent string) string {
	bus, err := comms.OpenReaderDir(dir)
	if err != nil {
		return ""
	}
	defer func() { _ = bus.Close() }()
	reason := ""
	var after events.Cursor
	for {
		page, last, err := comms.Export(bus, after, 500)
		if err != nil || len(page) == 0 {
			break
		}
		for _, e := range page {
			r := e.Record
			if r.Kind == comms.KindWake && r.Via == "socket" && r.State == comms.StateFailed && r.Err != "" && len(r.To) > 0 && r.To[0] == parent {
				reason = r.Err
			}
		}
		if last <= after {
			break
		}
		after = last
	}
	return reason
}
