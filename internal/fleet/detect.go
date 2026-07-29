package fleet

import (
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Assessment is the read-only verdict of a fleet scan.
type Assessment struct {
	// Total is the number of instances scanned.
	Total int
	// Alive is how many have a live tmux session.
	Alive int
	// Down is how many are recovery candidates (len(Candidates)).
	Down int
	// Skipped is how many were intentionally not running (stopped/queued/archived).
	Skipped int
	// MassDeath is true when the down set is large enough, both absolutely and
	// as a share of should-be-alive sessions, to read as a fleet-wide event
	// rather than one crashed session.
	MassDeath bool
	// Candidates are the down sessions in recovery order.
	Candidates []Candidate
	// Probes is how many confirming tmux probes were required.
	Probes int
}

// Detector finds sessions the registry believes are alive whose tmux session is
// gone. All I/O is behind function fields so tests never touch a tmux server.
type Detector struct {
	// TmuxExists probes one session on its own socket. Signature mirrors
	// session.Reviver.TmuxExists: the socket must be passed explicitly or
	// sessions on an isolated socket look dead on the default server.
	TmuxExists func(name, socketName string) bool
	// Sleep separates confirming probes.
	Sleep func(time.Duration)

	// ConfirmProbes / ConfirmDelay implement the "one miss is not proof of
	// death" rule (see DefaultConfirmProbes). Values below 1 are treated as 1.
	ConfirmProbes int
	ConfirmDelay  time.Duration

	// MinDead / DeadFraction tune the MassDeath verdict.
	MinDead      int
	DeadFraction float64

	// IncludeIdle adds status=idle sessions to the candidate set. Off by
	// default: a pane that died maps to error (or stopped for opencode), while
	// idle is also the status of a session that was added but never started, so
	// including it turns "recover the fleet" into "launch things the operator
	// never launched".
	IncludeIdle bool

	// Group, when set, restricts the scan to that group path and its
	// descendants. Everything outside is classified HealthSkipped.
	Group string
}

// NewDetector returns a Detector wired to real tmux probes and defaults.
func NewDetector() *Detector {
	return &Detector{
		// Argument order is flipped on purpose: tmux.HasSessionOnSocket takes
		// (socket, name) while the probe seam mirrors session.Reviver's
		// (name, socket) shape.
		TmuxExists: func(name, socketName string) bool {
			return tmux.HasSessionOnSocket(socketName, name)
		},
		Sleep:         time.Sleep,
		ConfirmProbes: DefaultConfirmProbes,
		ConfirmDelay:  DefaultConfirmDelay,
		MinDead:       DefaultMinDead,
		DeadFraction:  DefaultDeadFraction,
	}
}

// Assess classifies every instance and returns the verdict. It performs no
// writes and mutates no instance — safe to run on a live host at any time.
func (d *Detector) Assess(instances []*session.Instance) Assessment {
	probes := d.confirmProbes()
	as := Assessment{Probes: probes}

	// First pass: classify without probing anything that cannot be a candidate,
	// so a scan costs one tmux probe per should-be-alive session and zero for
	// the rest.
	type pending struct {
		inst   *session.Instance
		status string
	}
	var maybeDown []pending

	for _, inst := range instances {
		if inst == nil {
			continue
		}
		as.Total++
		status := string(inst.Status)

		switch health, _ := d.Classify(inst); health {
		case HealthSkipped:
			as.Skipped++
		case HealthAlive:
			as.Alive++
		default:
			maybeDown = append(maybeDown, pending{inst: inst, status: status})
		}
	}

	// Second pass: re-probe the misses. A session that reappears on any
	// confirming probe was never dead (tmux server restart race) and is counted
	// alive instead of being restarted.
	for round := 1; round < probes && len(maybeDown) > 0; round++ {
		if d.ConfirmDelay > 0 && d.Sleep != nil {
			d.Sleep(d.ConfirmDelay)
		}
		kept := maybeDown[:0]
		for _, p := range maybeDown {
			if d.exists(p.inst) {
				as.Alive++
				continue
			}
			kept = append(kept, p)
		}
		maybeDown = kept
	}

	for _, p := range maybeDown {
		as.Candidates = append(as.Candidates, Candidate{
			Instance: p.inst,
			Health:   HealthDown,
			Status:   p.status,
		})
	}
	as.Down = len(as.Candidates)

	// Recover oldest-first (stable by created-at, then title) so a partial
	// sweep is reproducible and the operator can predict what a --limit run
	// will touch.
	sort.SliceStable(as.Candidates, func(i, j int) bool {
		a, b := as.Candidates[i].Instance, as.Candidates[j].Instance
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.Title < b.Title
	})

	as.MassDeath = d.isMassDeath(as)
	return as
}

// isMassDeath applies both thresholds: an absolute floor (a single crash is not
// a fleet death) and a share of the should-be-alive population (30 down out of
// 500 healthy is a bad afternoon, not a fleet death).
func (d *Detector) isMassDeath(as Assessment) bool {
	minDead := d.MinDead
	if minDead <= 0 {
		minDead = DefaultMinDead
	}
	if as.Down < minDead {
		return false
	}
	shouldBeAlive := as.Down + as.Alive
	if shouldBeAlive == 0 {
		return false
	}
	frac := d.DeadFraction
	if frac <= 0 {
		frac = DefaultDeadFraction
	}
	return float64(as.Down)/float64(shouldBeAlive) >= frac
}

func (d *Detector) confirmProbes() int {
	if d.ConfirmProbes < 1 {
		return 1
	}
	return d.ConfirmProbes
}

// exists probes one session's liveness.
//
// A missing probe reports ALIVE, not dead. Every default in this package leans
// toward doing nothing: a Detector built without a probe (a future caller
// constructing Detector{} directly) would otherwise classify the entire fleet as
// down, and a `--yes` sweep on that assessment would restart every live session
// on the host. Failing this way makes such a bug a no-op instead.
func (d *Detector) exists(inst *session.Instance) bool {
	if d.TmuxExists == nil {
		return true
	}
	return d.TmuxExists(TmuxName(inst), inst.TmuxSocketName)
}

// Classify reports one session's recovery-oriented health, plus a human-readable
// reason when the verdict is HealthSkipped.
//
// It performs at most ONE liveness probe, so a HealthDown verdict from Classify
// alone is not yet proof of death — Assess is the entry point that applies the
// confirming re-probe (see DefaultConfirmProbes) before a session becomes a
// recovery candidate.
func (d *Detector) Classify(inst *session.Instance) (Health, string) {
	if inst == nil {
		return HealthSkipped, "no instance"
	}
	if !inst.ArchivedAt.IsZero() {
		return HealthSkipped, "archived"
	}
	if d.Group != "" && !inGroup(inst.GroupPath, d.Group) {
		return HealthSkipped, "outside --group"
	}
	if !d.shouldBeAlive(inst.Status) {
		return HealthSkipped, "status " + string(inst.Status)
	}
	if !inst.CanRestart() {
		return HealthSkipped, "not restartable"
	}
	if d.exists(inst) {
		return HealthAlive, ""
	}
	return HealthDown, ""
}

// shouldBeAlive reports whether a status is a claim that the session's process
// is running. StatusStopped/StatusQueued are operator intent and never
// recovered; StatusIdle is opt-in (see Detector.IncludeIdle).
func (d *Detector) shouldBeAlive(st session.Status) bool {
	switch st {
	case session.StatusRunning, session.StatusWaiting, session.StatusStarting, session.StatusError:
		return true
	case session.StatusIdle:
		return d.IncludeIdle
	}
	return false
}

// inGroup reports whether groupPath is want or a descendant of it.
func inGroup(groupPath, want string) bool {
	groupPath = strings.Trim(groupPath, "/")
	want = strings.Trim(want, "/")
	if want == "" {
		return true
	}
	return groupPath == want || strings.HasPrefix(groupPath, want+"/")
}

// TmuxName resolves the tmux session name to probe for an instance. Mirrors
// session.instanceTmuxName (unexported there): the attached tmux.Session is
// authoritative, with the title as the legacy fallback for records built
// without one.
func TmuxName(inst *session.Instance) string {
	if inst == nil {
		return ""
	}
	if ts := inst.GetTmuxSession(); ts != nil && ts.Name != "" {
		return ts.Name
	}
	return inst.Title
}
