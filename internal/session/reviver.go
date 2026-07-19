package session

import (
	"log/slog"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// RevivalClass categorizes an Instance's recoverability at scan time.
//
//   - ClassAlive    — tmux session exists AND our control pipe is alive.
//     The session is healthy; no action needed.
//   - ClassErrored  — tmux session exists but the control pipe is dead (or
//     Status == StatusError). Likely cause: SSH logout killed our inherited
//     pipe but the tmux server survived under its own user scope. Revivable.
//   - ClassDead     — tmux session does not exist. Server gone (OOM, reboot,
//     explicit tmux kill-session, or a systemd scope reap that took the whole
//     server). NOT auto-revived; user must explicit `session restart` because
//     we cannot distinguish intentional kill from crash.
type RevivalClass int

const (
	ClassAlive RevivalClass = iota
	ClassErrored
	ClassDead
)

func (c RevivalClass) String() string {
	switch c {
	case ClassAlive:
		return "alive"
	case ClassErrored:
		return "errored"
	case ClassDead:
		return "dead"
	}
	return "unknown"
}

// ReviveOutcome describes one instance's revive attempt.
type ReviveOutcome struct {
	InstanceID string
	Title      string
	Class      RevivalClass
	Revived    bool
	Err        error
	// CircuitOpen is true when the futility circuit breaker skipped this
	// instance's revive this sweep because prior revives never stabilized
	// (a wedged tmux server that only a manual restart recovers, #1579).
	CircuitOpen bool
}

// Reviver walks storage and re-establishes dead control pipes for instances
// whose underlying tmux server is still alive. See REPORT-D-auto-revive.md
// and .planning/v178-ssh-reviver/PLAN.md for design rationale.
//
// Fields are injectable so tests can stub out tmux and pipe checks without
// spawning real processes. Production code should use NewReviver() to get
// sensible defaults.
type Reviver struct {
	// TmuxExists receives the session's stored socket name so the probe
	// targets the right tmux server. Sessions created under an isolated
	// socket (Instance.TmuxSocketName != "") would otherwise appear "dead"
	// on the default server and the reviver would wrongly mark them gone.
	TmuxExists   func(name, socketName string) bool
	PipeAlive    func(name string) bool
	ReviveAction func(*Instance) error
	Stagger      time.Duration
	Log          *slog.Logger
	// Breaker bounds futile reconnect storms on a wedged tmux server (#1579).
	// nil disables the breaker entirely — exact legacy behavior, relied on by
	// unit tests that construct Reviver{} directly.
	Breaker *ReviveBreaker
}

// NewReviver returns a Reviver wired to real tmux + PipeManager primitives.
// Defaults: 500ms stagger between revives to avoid thundering herd on Claude
// cold-start rate limits when many sessions are errored simultaneously.
func NewReviver() *Reviver {
	return &Reviver{
		TmuxExists:   defaultTmuxExists,
		PipeAlive:    defaultPipeAlive,
		ReviveAction: defaultReviveAction,
		Stagger:      500 * time.Millisecond,
		Log:          sessionLog,
		// Process-global breaker: the TUI builds a fresh Reviver every 60s
		// sweep (internal/ui/home.go), so per-sweep state would never
		// accumulate. The breaker must outlive the Reviver to detect a
		// storm across sweeps. CLI one-shots run in a short-lived process,
		// so their global breaker starts empty and always probes.
		Breaker: globalReviveBreaker,
	}
}

// Classify decides which bucket an instance falls into at scan time.
//
// TODO(revive-liveness): after a tmux SERVER restart (OOM/SIGKILL +
// systemd Restart=on-failure, or a manual restart), a session that is in
// fact alive under the NEW server can briefly probe as not-found if the
// has-session call races the server coming back up. Today that yields
// ClassDead, which is never auto-revived — so a transient miss permanently
// strands a recoverable session until the user restarts it manually. A fix
// (single short retry on a confirmed-not-found probe, scoped to revive so it
// does not add latency to the shared HasSession hot path) is deferred: it is
// orthogonal to the data-loss race fixed here and carries its own regression
// risk for the watchPipe reconnect loop that shares the probe. The
// data-loss-critical half (revive never clobbering concurrently-added rows)
// is fixed via Storage.PersistRevivedInstances.
func (r *Reviver) Classify(inst *Instance) RevivalClass {
	name := instanceTmuxName(inst)
	if !r.TmuxExists(name, inst.TmuxSocketName) {
		return ClassDead
	}
	if inst.Status == StatusError || !r.PipeAlive(name) {
		return ClassErrored
	}
	return ClassAlive
}

// ReviveAll walks instances, classifies each, and triggers ReviveAction for
// those in ClassErrored. Calls are staggered by r.Stagger. Alive/dead entries
// do NOT consume a stagger slot — total wall clock scales with errored count,
// not total count.
//
// MUTATION INVARIANT (relied on by the CLI persist path, revive_cmd.go):
// ReviveAll mutates an instance ONLY when it both (a) classifies it ClassErrored
// AND (b) its ReviveAction succeeds (outcome.Revived == true). ClassAlive and
// ClassDead instances are classified and returned untouched — no status flip, no
// timestamp/socket normalization. The only field a successful revive writes is
// Instance.Status (StatusError → StatusRunning; see defaultReviveAction). The
// caller therefore persists exactly the Revived subset, status-only, and nothing
// else. If a future ReviveAction starts normalizing already-alive sessions or
// mutating other fields, this invariant — and the targeted persist in
// runReviveAll / PersistRevivedInstances — MUST be revisited so those mutations
// are not silently dropped.
func (r *Reviver) ReviveAll(instances []*Instance) []ReviveOutcome {
	if r.Breaker != nil {
		r.Breaker.Prune()
	}
	outcomes := make([]ReviveOutcome, 0, len(instances))
	firstRevive := true
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		outcomes = append(outcomes, r.reviveOneInternal(inst, &firstRevive))
	}
	return outcomes
}

// ReviveOne runs a single-instance revive cycle. Used by the CLI --name flag.
func (r *Reviver) ReviveOne(inst *Instance) ReviveOutcome {
	first := true
	return r.reviveOneInternal(inst, &first)
}

// reviveOneInternal does the actual classify + action + stagger dance.
// firstRevive is a pointer so the caller can reset it across a batch: the
// first actual revive runs immediately; subsequent ones sleep Stagger first.
func (r *Reviver) reviveOneInternal(inst *Instance, firstRevive *bool) ReviveOutcome {
	class := r.Classify(inst)
	out := ReviveOutcome{
		InstanceID: inst.ID,
		Title:      inst.Title,
		Class:      class,
	}

	// Feed the classification to the breaker every sweep (not just for
	// errored instances) so ClassAlive can reset a session's futility state
	// and so futility from a prior sweep's revive is judged before we decide
	// whether to attempt another one.
	attempt := true
	if r.Breaker != nil {
		attempt = r.Breaker.OnClassify(inst.ID, inst.Title, class)
	}

	if class != ClassErrored {
		return out
	}

	// Circuit open and cooling down: skip the doomed reconnect. This is the
	// storm brake — on a wedged tmux server the breaker keeps us from
	// respawning the same dead pipes every sweep (#1579).
	if !attempt {
		out.CircuitOpen = true
		return out
	}

	if !*firstRevive && r.Stagger > 0 {
		time.Sleep(r.Stagger)
	}
	*firstRevive = false

	if err := r.ReviveAction(inst); err != nil {
		out.Err = err
		if r.Breaker != nil {
			// A failed action is immediately futile — count it toward the trip.
			r.Breaker.AfterRevive(inst.ID, inst.Title, err)
		}
		if r.Log != nil {
			r.Log.Warn("reviver_action_failed",
				slog.String("title", inst.Title),
				slog.String("error", err.Error()))
		}
		return out
	}
	if r.Breaker != nil {
		// Action "succeeded"; mark pending so the next sweep can tell whether
		// the session actually stabilized or is still errored (futile).
		r.Breaker.AfterRevive(inst.ID, inst.Title, nil)
	}
	out.Revived = true
	if r.Log != nil {
		r.Log.Info("reviver_respawned",
			slog.String("title", inst.Title),
			slog.String("instance_id", inst.ID))
	}
	return out
}

// instanceTmuxName extracts the tmux session name from an Instance. Falls
// back to Title if no tmux.Session is attached (e.g., constructed in tests
// without NewInstance).
func instanceTmuxName(inst *Instance) string {
	if ts := inst.GetTmuxSession(); ts != nil {
		return ts.Name
	}
	return inst.Title
}

// defaultTmuxExists queries the tmux server for session presence. Returns
// false for any failure (tmux not installed, server not running, session
// doesn't exist).
func defaultTmuxExists(name, socketName string) bool {
	return tmux.HasSessionOnSocket(socketName, name)
}

// defaultPipeAlive consults the global PipeManager. Returns false if the
// manager is uninitialized (control pipes disabled) or the pipe is not
// alive.
func defaultPipeAlive(name string) bool {
	pm := tmux.GetPipeManager()
	if pm == nil {
		return false
	}
	return pm.IsConnected(name)
}

// defaultReviveAction re-establishes the control pipe for an errored instance.
// When PipeManager is available (TUI mode), reconnects the pipe via Connect.
// In CLI one-shot mode (no PipeManager), falls back to a status-only heal:
// the Classify gate already confirmed the tmux server is alive, so flipping
// StatusError → StatusRunning reflects reality for the next TUI launch.
func defaultReviveAction(inst *Instance) error {
	pm := tmux.GetPipeManager()
	name := instanceTmuxName(inst)

	if pm != nil {
		if err := pm.Connect(name, inst.TmuxSocketName); err != nil {
			return err
		}
	}
	// Status heal runs in both TUI and CLI modes — Classify already verified
	// tmux is alive, so a StatusError reading is stale.
	if inst.Status == StatusError {
		inst.Status = StatusRunning
	}
	return nil
}
