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
	// AuthHeld is true when the revive was skipped because the session's agent
	// cannot authenticate (see auth_hold.go). Distinct from CircuitOpen: this is
	// not futility to back off from, it is a condition only a human can clear.
	AuthHeld bool
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
	// AuthHeld reports whether an instance is held out of automatic recovery
	// because its agent cannot authenticate (see auth_hold.go). nil disables the
	// check — legacy behavior for unit tests that construct Reviver{} directly.
	AuthHeld func(*Instance) (bool, string)
	// ConfirmAfter makes a pipe-dead verdict a two-sample one: an instance
	// whose tmux server is up and whose stored status is not error, but whose
	// pipe read dead, is read again after this delay and acted on only if the
	// pipe is still dead. One wait per sweep, not per instance. Zero disables
	// (legacy single-sample behaviour, relied on by unit tests).
	//
	// The reading it guards against is a real one but not evidence of a dead
	// session: the TUI attaches its control pipes shortly after boot, so a
	// sweep that samples during a start or an in-place restart sees every
	// pipe dead and "respawns" live sessions (the v1.16.11 rollout respawned
	// the maintainer's live session twice right after tui_restarted).
	ConfirmAfter time.Duration
	// sleep is time.Sleep unless a test replaces it.
	sleep func(time.Duration)
}

// DefaultReviveConfirmAfter is NewReviver's ConfirmAfter: long enough for
// the TUI's pipe reconciler (500ms interval) to have attached the pipes it
// wants after a boot, short enough not to matter for a real revive.
const DefaultReviveConfirmAfter = 3 * time.Second

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
		Breaker:      globalReviveBreaker,
		AuthHeld:     defaultBootAuthHeld,
		ConfirmAfter: DefaultReviveConfirmAfter,
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
	tmuxAlive := r.TmuxExists(name, inst.TmuxSocketName)
	// Read the pipe whenever the server is up, even when the stored status alone
	// already settles the verdict: the READING is the evidence #1705 asked for, and
	// a log line that omits it cannot answer "was the session actually dead?" after
	// the fact. IsConnected is an in-memory map lookup, so this costs nothing.
	pipeAlive := false
	if tmuxAlive && r.PipeAlive != nil {
		pipeAlive = r.PipeAlive(name)
	}

	class := ClassAlive
	switch {
	case !tmuxAlive:
		class = ClassDead
	case inst.Status == StatusError || !pipeAlive:
		class = ClassErrored
	}
	r.logClassify(inst, name, tmuxAlive, pipeAlive, class)
	return class
}

// logClassify records the readings behind a classification, not just its outcome.
//
// Issue #1705 was a live conductor restarted as if it were dead, and the
// investigation stalled because only the OUTCOME was recoverable afterwards — the
// readings that produced it were never written down anywhere an operator could
// retrieve. So every non-alive verdict states its evidence: tmux liveness, the
// control-pipe reading, the stored status it was judged against, and when. Alive
// verdicts stay at debug level; they are the overwhelming majority and carry no
// diagnostic value.
func (r *Reviver) logClassify(inst *Instance, name string, tmuxAlive, pipeAlive bool, class RevivalClass) {
	if r.Log == nil {
		return
	}
	attrs := []any{
		slog.String("title", inst.Title),
		slog.String("instance_id", inst.ID),
		slog.String("tmux_session", name),
		slog.Bool("tmux_alive", tmuxAlive),
		slog.Bool("pipe_alive", pipeAlive),
		slog.String("stored_status", string(inst.Status)),
		slog.String("class", class.String()),
		slog.Time("sampled_at", time.Now()),
	}
	if class == ClassAlive {
		r.Log.Debug("reviver_classify", attrs...)
		return
	}
	r.Log.Info("reviver_classify", attrs...)
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
	live := make([]*Instance, 0, len(instances))
	for _, inst := range instances {
		if inst != nil {
			live = append(live, inst)
		}
	}
	classes := make([]RevivalClass, len(live))
	for i, inst := range live {
		classes[i] = r.Classify(inst)
	}
	r.confirmPipeDead(live, classes)
	outcomes := make([]ReviveOutcome, 0, len(live))
	firstRevive := true
	for i, inst := range live {
		outcomes = append(outcomes, r.reviveOneInternal(inst, classes[i], &firstRevive))
	}
	return outcomes
}

// ReviveOne runs a single-instance revive cycle. Used by the CLI --name flag.
func (r *Reviver) ReviveOne(inst *Instance) ReviveOutcome {
	classes := []RevivalClass{r.Classify(inst)}
	r.confirmPipeDead([]*Instance{inst}, classes)
	first := true
	return r.reviveOneInternal(inst, classes[0], &first)
}

// pipeDeadOnly reports whether class rests on the pipe reading alone: the
// server is up and the stored status did not already decide the verdict.
func pipeDeadOnly(inst *Instance, class RevivalClass) bool {
	return class == ClassErrored && inst.Status != StatusError
}

// confirmPipeDead re-reads the pipe of every pipe-dead-only instance once,
// ConfirmAfter later, and downgrades to ClassAlive those whose pipe is up
// by then. A single wait covers the whole batch.
func (r *Reviver) confirmPipeDead(instances []*Instance, classes []RevivalClass) {
	if r.ConfirmAfter <= 0 || r.PipeAlive == nil {
		return
	}
	var pending []int
	for i, inst := range instances {
		if pipeDeadOnly(inst, classes[i]) {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 {
		return
	}
	sleep := r.sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	sleep(r.ConfirmAfter)
	for _, i := range pending {
		inst := instances[i]
		if !r.PipeAlive(instanceTmuxName(inst)) {
			continue
		}
		classes[i] = ClassAlive
		if r.Log != nil {
			r.Log.Info("reviver_pipe_dead_transient",
				slog.String("title", inst.Title),
				slog.String("instance_id", inst.ID),
				slog.Duration("confirm_after", r.ConfirmAfter))
		}
	}
}

// reviveOneInternal does the breaker + action + stagger dance for an
// already classified instance. firstRevive is a pointer so the caller can
// reset it across a batch: the first actual revive runs immediately;
// subsequent ones sleep Stagger first.
func (r *Reviver) reviveOneInternal(inst *Instance, class RevivalClass, firstRevive *bool) ReviveOutcome {
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

	// An auth-held session is not revivable by machine. Healing its status back
	// to running (which is all defaultReviveAction can do for a session whose
	// agent has exited) would ERASE the one honest signal the user needs — the
	// auth-401 substate — and hand the fleet back a green light it has not
	// earned. Skip, and leave the hold for a human to clear.
	if r.AuthHeld != nil {
		if held, remedy := r.AuthHeld(inst); held {
			out.AuthHeld = true
			if r.Log != nil {
				r.Log.Warn("reviver_auth_held_skip",
					slog.String("title", inst.Title),
					slog.String("instance_id", inst.ID),
					slog.String("remedy", remedy))
			}
			return out
		}
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
