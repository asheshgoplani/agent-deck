package send

import (
	"fmt"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Post-spawn settle gate.
//
// `agent-deck session restart <id>` respawns the pane and returns as soon as
// tmux has swapped the process — the replacement agent is still booting. The
// `session send` that follows runs in a SEPARATE process, and every readiness
// signal it can reach is blind to that fact:
//
//   - tmux.Session.startupAt (the "starting" status window) is set by
//     Start()/RespawnPane() in the process that performed the spawn, and
//     ReconnectSession* explicitly zeroes it for every other process. So a
//     booting agent that shows neither spinner nor prompt is classified
//     "waiting" — the exact status WaitForAgentReady accepts as ready.
//   - Claude paints its composer glyph before its input handler is armed
//     (issue #616) and before its slash-command router registers (issue #966),
//     so "composer visible" alone is not "will accept keystrokes".
//
// The result is a send that types into a half-mounted TUI and is silently
// discarded, which operators worked around with a blind ~8s sleep between
// `restart` and `send`.
//
// WaitForSpawnSettle replaces that blind sleep with a bounded, probing gate:
// it engages only when the target was (re)spawned recently (the caller checks
// the durable per-instance spawn stamp), and it clears as soon as the agent's
// prompt has been continuously visible for StableFor AND the spawn is at least
// MinAge old.

// SpawnSettleDefaults are the production budgets for the post-spawn gate.
const (
	// DefaultSpawnSettleWindow is how long after a spawn a send is treated as
	// a cold start at all. Beyond it the gate is skipped entirely, so steady
	// state sends pay nothing.
	DefaultSpawnSettleWindow = 90 * time.Second
	// DefaultSpawnSettleMinAge is the floor on wall-clock age of the spawn
	// before we type. Claude's composer can render while the React input
	// handler is still mounting; no amount of pane-watching sees that.
	DefaultSpawnSettleMinAge = 5 * time.Second
	// DefaultSpawnSettleStableFor is how long the agent's UI must be
	// continuously on screen. A pane mid-resume-replay flickers; a mounted one
	// does not.
	DefaultSpawnSettleStableFor = 1500 * time.Millisecond
	// DefaultSpawnSettlePoll is the probe cadence.
	DefaultSpawnSettlePoll = 200 * time.Millisecond
	// DefaultSpawnSettleTimeout bounds the gate so a wedged or unusual pane
	// cannot hold a send forever.
	DefaultSpawnSettleTimeout = 20 * time.Second
)

// SpawnSettleOptions bounds WaitForSpawnSettle. Zero fields take the
// DefaultSpawnSettle* budgets.
type SpawnSettleOptions struct {
	MinAge    time.Duration
	StableFor time.Duration
	Poll      time.Duration
	Timeout   time.Duration
}

func (o SpawnSettleOptions) withDefaults() SpawnSettleOptions {
	if o.MinAge <= 0 {
		o.MinAge = DefaultSpawnSettleMinAge
	}
	if o.StableFor <= 0 {
		o.StableFor = DefaultSpawnSettleStableFor
	}
	if o.Poll <= 0 {
		o.Poll = DefaultSpawnSettlePoll
	}
	if o.Timeout <= 0 {
		o.Timeout = DefaultSpawnSettleTimeout
	}
	return o
}

// SpawnSettleDue reports whether a send to a session spawned at spawnedAt
// should run the settle gate. A zero spawnedAt (no stamp on disk) means
// "unknown" and skips the gate — we never guess a session is cold.
func SpawnSettleDue(spawnedAt time.Time, window time.Duration) bool {
	if spawnedAt.IsZero() {
		return false
	}
	if window <= 0 {
		window = DefaultSpawnSettleWindow
	}
	age := time.Since(spawnedAt)
	// A stamp from the future (clock skew between the restarting process and
	// this one) still means "just spawned".
	return age < window
}

// WaitForSpawnSettle holds delivery until a freshly (re)spawned agent can
// actually accept input: its UI has been continuously on screen for StableFor
// and the spawn is at least MinAge old.
//
// sleep is injected so tests can drive the loop; pass time.Sleep.
//
// On timeout it returns an error, but callers are expected to treat that as
// advisory and send anyway: the gate improves timing, while post-send delivery
// verification (issues #876/#1413) remains the authority on whether the
// message actually landed. Returning the error rather than swallowing it keeps
// the "we sent into an unsettled pane" fact reportable.
func WaitForSpawnSettle(
	target AgentReadyChecker,
	tool string,
	gates PromptGates,
	spawnedAt time.Time,
	opts SpawnSettleOptions,
	sleep func(time.Duration),
) error {
	opts = opts.withDefaults()
	if sleep == nil {
		sleep = time.Sleep
	}

	deadline := time.Now().Add(opts.Timeout)
	var mountedSince time.Time

	for {
		if paneShowsMountedUI(target, tool, gates) {
			if mountedSince.IsZero() {
				mountedSince = time.Now()
			}
		} else {
			mountedSince = time.Time{}
		}

		agedOut := time.Since(spawnedAt) >= opts.MinAge
		stable := !mountedSince.IsZero() && time.Since(mountedSince) >= opts.StableFor
		if agedOut && stable {
			return nil
		}

		if !time.Now().Before(deadline) {
			return fmt.Errorf(
				"agent (tool=%s) had not settled %s after spawn within %s (UI stable: %t)",
				tool, opts.MinAge, opts.Timeout, stable,
			)
		}
		sleep(opts.Poll)
	}
}

// paneShowsMountedUI reports whether the pane proves a mounted, running agent
// UI — either its input prompt is on screen, or it is visibly generating
// ("esc to interrupt"), which only a mounted TUI renders.
//
// The busy case matters because the readiness predicates deliberately reject a
// busy pane: without it, a freshly restarted session that picked work straight
// back up would hold the gate for its full timeout even though its UI is
// demonstrably alive.
func paneShowsMountedUI(target AgentReadyChecker, tool string, gates PromptGates) bool {
	raw, err := target.CapturePaneFresh()
	if err != nil {
		return false
	}
	content := tmux.StripANSI(raw)
	return paneLooksBusy(content) || contentShowsReadyPrompt(content, tool, gates)
}
