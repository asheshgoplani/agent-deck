// Tests for the per-session ownership lease that gates service-mode unit
// teardown (issue #1721).
//
// The landing gate on the issue is behavioural:
//
//  1. two live sibling sessions share the service-mode unit,
//  2. removing one never stops, respawns, or changes the other,
//  3. stale-generation teardown cannot affect the replacement generation,
//  4. unit shutdown occurs only after exclusive ownership is established.
//
// Those four are pinned here without systemd and without a live tmux
// server: server occupancy, unit state, pid liveness and systemctl
// presence all come in through package seams, so the cases run identically
// on the macOS CI jobs (no systemd at all) and on Linux.
package tmux

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// liveSet builds the authoritative "sessions on this socket" set.
func liveSet(names ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, n := range names {
		set[n] = struct{}{}
	}
	return set
}

// withServiceUnitSeams pins every external dependency of the gate:
// systemctl presence, unit state, server occupancy and pid liveness. It
// returns a pointer to the recorded systemctl argv so a test can assert
// that NO stop was issued (the whole point of the gate).
func withServiceUnitSeams(
	t *testing.T,
	state serviceUnitState,
	stateErr error,
	live map[string]struct{},
	liveErr error,
) *[][]string {
	t.Helper()

	origExec := execCommand
	origLook := systemctlLookPath
	origState := readServiceUnitState
	origLive := liveSessionsOnSocket
	t.Cleanup(func() {
		execCommand = origExec
		systemctlLookPath = origLook
		readServiceUnitState = origState
		liveSessionsOnSocket = origLive
	})

	var calls [][]string
	execCommand = func(name string, arg ...string) *exec.Cmd {
		calls = append(calls, append([]string{name}, arg...))
		// Never touch the host's systemd: run a harmless no-op instead.
		return exec.Command("true")
	}
	systemctlLookPath = func() error { return nil }
	readServiceUnitState = func(string) (serviceUnitState, error) { return state, stateErr }
	liveSessionsOnSocket = func(string) (map[string]struct{}, error) { return live, liveErr }

	return &calls
}

// systemctlStopCalls returns the recorded `systemctl --user stop` argv.
func systemctlStopCalls(calls [][]string) [][]string {
	var stops [][]string
	for _, c := range calls {
		if len(c) >= 4 && c[0] == "systemctl" && c[1] == "--user" && c[2] == "stop" {
			stops = append(stops, c)
		}
	}
	return stops
}

// --- gate #1: the shared unit is reachable in the first place -------------

// TestServiceUnitName_TwoSessionsCanShareOneUnit is the precondition the
// issue describes: unit names are derived from a sanitized, 48-byte-
// truncated session name, so two DISTINCT sessions can map to one unit.
// Unconditional teardown of that unit is therefore never safe.
func TestServiceUnitName_TwoSessionsCanShareOneUnit(t *testing.T) {
	a := "agentdeck_release_candidate_verification_worker_alpha_a1b2c3d4"
	b := "agentdeck_release_candidate_verification_worker_beta_e5f6a7b8"
	require.NotEqual(t, a, b, "sessions must be distinct for the case to mean anything")

	require.Equal(t, ServiceUnitName(a), ServiceUnitName(b),
		"long sibling session names collapse to ONE unit name — the shared-unit precondition")
	require.True(t, strings.HasSuffix(ServiceUnitName(a), ".service"))
	require.Equal(t, serviceUnitBase(a)+".service", ServiceUnitName(a),
		"spawn (base) and teardown (base+.service) must share one derivation")
}

// --- gate #2: removing one session never touches the sibling --------------

// TestStopServiceUnitOwned_SiblingOnSharedServer_IssuesNoStop is the core
// case: our session is gone but a sibling still lives on the tmux server
// the unit supervises. No systemctl stop may be issued — KillMode=
// control-group would take the sibling down with the server.
func TestStopServiceUnitOwned_SiblingOnSharedServer_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "active", MainPID: 4242, MainPIDAlive: true},
		nil,
		liveSet("agentdeck_sibling_b2b2b2b2"),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{
		SessionName:      "agentdeck_removed_a1a1a1a1",
		RetiredServerPID: 4242,
	})

	assert.False(t, dec.Stopped, "a co-tenanted unit must never be stopped")
	assert.Equal(t, UnitStopSkipServerShared, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls),
		"no systemctl stop may be issued while a sibling session shares the server")
}

// TestStopServiceUnitOwned_DeadUnitButSiblingRegistered: even when the
// unit has no live main process, a sibling still on the socket blocks the
// stop — stopping would cancel the restart protection the sibling relies
// on ("never ... respawns, or changes the other").
func TestStopServiceUnitOwned_DeadUnitButSiblingLive_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "inactive", MainPID: 0, MainPIDAlive: false},
		nil,
		liveSet("agentdeck_sibling_b2b2b2b2"),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipServerShared, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// TestStopServiceUnitOwned_OwnSessionStillLive_IssuesNoStop: teardown did
// not complete. Stopping the unit now would substitute a cgroup-wide kill
// for a session kill.
func TestStopServiceUnitOwned_OwnSessionStillLive_IssuesNoStop(t *testing.T) {
	const name = "agentdeck_removed_a1a1a1a1"
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "active", MainPID: 4242, MainPIDAlive: true},
		nil,
		liveSet(name),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: name, RetiredServerPID: 4242})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipSessionStillLive, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// --- gate #3: stale teardown cannot hit the replacement generation -------

// TestStopServiceUnitOwned_ReplacementGeneration_IssuesNoStop: the pid we
// retired is gone and Restart=on-failure already brought up a NEW tmux
// server generation. A stop now would kill that replacement.
func TestStopServiceUnitOwned_ReplacementGeneration_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "active", MainPID: 9999, MainPIDAlive: true},
		nil,
		liveSet(), // replacement server has no agent-deck session (yet)
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{
		SessionName:      "agentdeck_removed_a1a1a1a1",
		RetiredServerPID: 4242, // the generation we killed, not 9999
	})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipReplacementGeneration, dec.Reason,
		"a live main pid that is not the generation we retired is a replacement")
	assert.Empty(t, systemctlStopCalls(*calls))
}

// TestStopServiceUnitOwned_StartJobInFlight_IssuesNoStop: systemd is
// activating the unit, so MainPID is not published yet. A dead-pid reading
// must not be mistaken for "nothing left to protect".
func TestStopServiceUnitOwned_StartJobInFlight_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "activating", MainPID: 0, MainPIDAlive: false},
		nil,
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipStartJobInFlight, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// TestStopServiceUnitOwned_UnitProcessAlive_IssuesNoStop: the supervised
// process is alive and we have no generation identity to attribute it to.
// Unproven ownership must skip.
func TestStopServiceUnitOwned_UnitProcessAlive_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "active", MainPID: 4242, MainPIDAlive: true},
		nil,
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipProcessAlive, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// --- indeterminate evidence is never treated as "safe" -------------------

// TestStopServiceUnitOwned_IndeterminateSessionProbe_IssuesNoStop: a
// wedged server makes the occupancy probe time out. ListSessionNamesOnSocket's
// contract says treat that as "assume alive", never as "no sessions".
func TestStopServiceUnitOwned_IndeterminateSessionProbe_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "inactive", MainPID: 0, MainPIDAlive: false},
		nil,
		nil,
		errors.New("probe timed out"),
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipIndeterminate, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// TestStopServiceUnitOwned_UnreadableUnitState_IssuesNoStop: systemctl show
// failed, so the unit's state is unknown. Skip.
func TestStopServiceUnitOwned_UnreadableUnitState_IssuesNoStop(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{},
		errors.New("systemctl show: connection refused"),
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipIndeterminate, dec.Reason)
	assert.Empty(t, systemctlStopCalls(*calls))
}

// TestStopServiceUnitOwned_EmptySessionName_NeverGuessesAUnit: an instance
// with no tmux session must not resolve to the sanitizer's "session"
// fallback unit and stop somebody else's unit.
func TestStopServiceUnitOwned_EmptySessionName_NeverGuessesAUnit(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "inactive"},
		nil,
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "  "})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipNoTmuxSession, dec.Reason)
	assert.Empty(t, dec.Unit, "no unit may be derived from an empty session name")
	assert.Empty(t, *calls, "no systemctl call at all for a session-less instance")
}

// TestStopServiceUnitOwned_NoSystemctl_IsANoOp: non-systemd hosts (every
// macOS user) must short-circuit without touching anything.
func TestStopServiceUnitOwned_NoSystemctl_IsANoOp(t *testing.T) {
	calls := withServiceUnitSeams(t, serviceUnitState{}, nil, liveSet(), nil)
	systemctlLookPath = func() error { return exec.ErrNotFound }

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: "agentdeck_removed_a1a1a1a1"})

	assert.False(t, dec.Stopped)
	assert.Equal(t, UnitStopSkipNoSystemctl, dec.Reason)
	assert.Equal(t, ServiceUnitName("agentdeck_removed_a1a1a1a1"), dec.Unit)
	assert.Empty(t, *calls)
}

// --- gate #4: shutdown only once ownership is established ----------------

// TestStopServiceUnitOwned_ExclusiveOwner_StopsAndResetsFailed: the server
// is empty, the unit has no live process and no start job is in flight —
// ownership is exclusive, so removal stays terminal (Restart=on-failure
// must not resurrect the session).
func TestStopServiceUnitOwned_ExclusiveOwner_StopsAndResetsFailed(t *testing.T) {
	const name = "agentdeck_removed_a1a1a1a1"
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "failed", MainPID: 0, MainPIDAlive: false},
		nil,
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{SessionName: name, RetiredServerPID: 4242})

	require.True(t, dec.Stopped, "exclusive ownership must retire the unit")
	assert.Equal(t, UnitStopReasonExclusive, dec.Reason)
	unit := ServiceUnitName(name)
	assert.Equal(t, [][]string{
		{"systemctl", "--user", "stop", unit},
		{"systemctl", "--user", "reset-failed", unit},
	}, *calls, "exclusive teardown is exactly stop + reset-failed on OUR unit")
}

// TestStopServiceUnitOwned_ExclusiveOwner_OurGenerationStillTracked: the
// unit's recorded main pid IS the generation we retired and it is dead.
// Still exclusive — stop is allowed.
func TestStopServiceUnitOwned_ExclusiveOwner_DeadOwnGeneration(t *testing.T) {
	calls := withServiceUnitSeams(t,
		serviceUnitState{ActiveState: "active", MainPID: 4242, MainPIDAlive: false},
		nil,
		liveSet(),
		nil,
	)

	dec := StopServiceUnitOwned(ServiceUnitOwnership{
		SessionName:      "agentdeck_removed_a1a1a1a1",
		RetiredServerPID: 4242,
	})

	require.True(t, dec.Stopped)
	assert.Equal(t, UnitStopReasonExclusive, dec.Reason)
	assert.Len(t, systemctlStopCalls(*calls), 1)
}

// --- predicate + parser units -------------------------------------------

// TestEvaluateServiceUnitOwnership_Table pins the predicate itself, including
// precedence between the skip reasons.
func TestEvaluateServiceUnitOwnership_Table(t *testing.T) {
	const self = "agentdeck_self_a1a1a1a1"

	cases := []struct {
		name       string
		own        ServiceUnitOwnership
		state      serviceUnitState
		live       map[string]struct{}
		liveErr    error
		wantStop   bool
		wantReason string
	}{
		{
			name:       "indeterminate probe wins over everything",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "inactive"},
			liveErr:    errors.New("timeout"),
			wantReason: UnitStopSkipIndeterminate,
		},
		{
			name:       "own session still live wins over sibling count",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "inactive"},
			live:       liveSet(self, "agentdeck_other_b2b2b2b2"),
			wantReason: UnitStopSkipSessionStillLive,
		},
		{
			name:       "sibling blocks even when unit looks idle",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "inactive"},
			live:       liveSet("agentdeck_other_b2b2b2b2"),
			wantReason: UnitStopSkipServerShared,
		},
		{
			name:       "non-agentdeck session on the shared server also blocks",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "inactive"},
			live:       liveSet("my-own-tmux-session"),
			wantReason: UnitStopSkipServerShared,
		},
		{
			name:       "start job in flight blocks",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "activating"},
			live:       liveSet(),
			wantReason: UnitStopSkipStartJobInFlight,
		},
		{
			name:       "replacement generation blocks",
			own:        ServiceUnitOwnership{SessionName: self, RetiredServerPID: 1},
			state:      serviceUnitState{ActiveState: "active", MainPID: 2, MainPIDAlive: true},
			live:       liveSet(),
			wantReason: UnitStopSkipReplacementGeneration,
		},
		{
			name:       "live process without generation identity blocks",
			own:        ServiceUnitOwnership{SessionName: self},
			state:      serviceUnitState{ActiveState: "active", MainPID: 2, MainPIDAlive: true},
			live:       liveSet(),
			wantReason: UnitStopSkipProcessAlive,
		},
		{
			name:       "exclusive owner",
			own:        ServiceUnitOwnership{SessionName: self, RetiredServerPID: 1},
			state:      serviceUnitState{ActiveState: "failed"},
			live:       liveSet(),
			wantStop:   true,
			wantReason: UnitStopReasonExclusive,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stop, reason := evaluateServiceUnitOwnership(tc.own, tc.state, tc.live, tc.liveErr)
			assert.Equal(t, tc.wantStop, stop)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}

// TestParseServiceUnitState pins the systemctl show parsing, including the
// not-found shape (MainPID=0) and a pid that is not alive.
func TestParseServiceUnitState(t *testing.T) {
	orig := unitPIDAlive
	unitPIDAlive = func(pid int) bool { return pid == 4242 }
	t.Cleanup(func() { unitPIDAlive = orig })

	alive := parseServiceUnitState("ActiveState=active\nMainPID=4242\n")
	assert.Equal(t, "active", alive.ActiveState)
	assert.Equal(t, 4242, alive.MainPID)
	assert.True(t, alive.MainPIDAlive)

	notFound := parseServiceUnitState("ActiveState=inactive\nMainPID=0\n")
	assert.Equal(t, "inactive", notFound.ActiveState)
	assert.Zero(t, notFound.MainPID)
	assert.False(t, notFound.MainPIDAlive)

	stale := parseServiceUnitState("MainPID=777\nActiveState=active\n")
	assert.Equal(t, 777, stale.MainPID)
	assert.False(t, stale.MainPIDAlive, "a recorded pid that is gone is not alive")
}

// TestSessionServiceUnitOwnership_CapturesSocketAndName: the snapshot the
// remove path takes must carry the socket (so occupancy is probed on the
// right server) and the session name (so the unit is derived, not guessed).
func TestSessionServiceUnitOwnership_CapturesSocketAndName(t *testing.T) {
	s := NewSession("ownership-snapshot", "/tmp")
	s.SocketName = "ad-test-socket"

	own := s.ServiceUnitOwnership()
	assert.Equal(t, s.Name, own.SessionName)
	assert.Equal(t, "ad-test-socket", own.SocketName)

	var nilSession *Session
	assert.Equal(t, ServiceUnitOwnership{}, nilSession.ServiceUnitOwnership(),
		"a nil session must yield the always-skip zero value")
}
