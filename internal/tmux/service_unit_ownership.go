// Package tmux — per-session ownership lease for service-mode unit
// teardown (issue #1721).
//
// Service-mode sessions (LaunchAs="service") are spawned as transient
// systemd-user units:
//
//	systemd-run --user --unit agentdeck-tmux-<session>.service \
//	    --property=Restart=on-failure ... tmux new-session -d -s <session>
//
// The unit supervises a tmux SERVER, not a tmux session. On the default
// socket every agent-deck session shares ONE server, and that server's
// cgroup is anchored by whichever session's unit happened to spawn it
// first. `agent-deck remove` used to `systemctl --user stop` the unit
// derived from the removed session's name unconditionally, which meant a
// single deletion could:
//
//  1. stop the shared server and kill every sibling session on it
//     (KillMode=control-group takes the whole cgroup),
//  2. cancel the restart protection a sibling still relies on, or
//  3. tear down a REPLACEMENT generation that Restart=on-failure had
//     already brought up after the generation we meant to retire died.
//
// Name derivation makes co-tenancy worse, not better:
// sanitizeSystemdUnitComponent lowercases, collapses every non-alphanumeric
// byte to '-' and truncates at 48 bytes, so two distinct sessions with long
// similar titles can map to the SAME unit name.
//
// StopServiceUnitOwned replaces the unconditional stop with an ownership
// lease check. The unit is retired ONLY when the terminating lifecycle is
// proven to be its exclusive owner: no other live session remains on the
// server, the unit has no live main process, and no start job is in flight.
// Anything indeterminate skips the stop — leaving a transient unit behind
// is recoverable, killing a sibling session is not.
package tmux

import (
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// serviceUnitBase returns the transient systemd-user unit BASE name used
// by both spawn forms (scope mode uses the base, service mode appends
// ".service"). Single derivation point so spawn and teardown cannot drift.
func serviceUnitBase(sessionName string) string {
	return "agentdeck-tmux-" + sanitizeSystemdUnitComponent(sessionName)
}

// ServiceUnitName returns the transient systemd-user service unit name a
// service-mode spawn uses for the given tmux session name. Exported so the
// teardown path and tests derive it exactly like startCommandSpec does.
func ServiceUnitName(sessionName string) string {
	return serviceUnitBase(sessionName) + ".service"
}

// Reasons recorded on a ServiceUnitDecision. Stable strings — they appear
// in logs and are asserted by tests.
const (
	// UnitStopReasonExclusive: ownership proven, unit stopped.
	UnitStopReasonExclusive = "exclusive_owner"
	// UnitStopSkipNoTmuxSession: caller had no tmux session name, so no
	// unit can be attributed. Never guess a unit name.
	UnitStopSkipNoTmuxSession = "no_tmux_session"
	// UnitStopSkipNoSystemctl: host has no systemctl — nothing to stop.
	UnitStopSkipNoSystemctl = "no_systemctl"
	// UnitStopSkipIndeterminate: the unit state or the server session list
	// could not be read. Ownership unproven → never stop.
	UnitStopSkipIndeterminate = "ownership_indeterminate"
	// UnitStopSkipSessionStillLive: our own session is still on the server,
	// so teardown did not complete; stopping now would be the shared-unit
	// hammer instead of a session kill.
	UnitStopSkipSessionStillLive = "session_still_live"
	// UnitStopSkipServerShared: other live sessions remain on the tmux
	// server this unit supervises — the unit is co-tenanted (gate #1/#2).
	UnitStopSkipServerShared = "tmux_server_shared"
	// UnitStopSkipStartJobInFlight: systemd is currently (re)starting the
	// unit; MainPID is not published yet, so a stop could kill a
	// replacement generation mid-spawn (gate #3).
	UnitStopSkipStartJobInFlight = "unit_start_job_in_flight"
	// UnitStopSkipReplacementGeneration: the unit's live main process is a
	// DIFFERENT generation than the one we retired (gate #3).
	UnitStopSkipReplacementGeneration = "replacement_generation"
	// UnitStopSkipProcessAlive: the unit still supervises a live process we
	// cannot prove is exclusively ours.
	UnitStopSkipProcessAlive = "unit_process_alive"
	// UnitStopSkipOwnershipUnproven means the exact socket/PID/starttime/
	// cgroup evidence does not bind the retired server to this unit.
	UnitStopSkipOwnershipUnproven = "ownership_unproven"
	// UnitStopSkipUnsafeKillMode means an existing unit can still kill its
	// whole cgroup. It is never stopped by this path, even if it looks empty:
	// a sibling may attach after the occupancy probe.
	UnitStopSkipUnsafeKillMode = "unsafe_unit_kill_mode"
)

// ServiceUnitOwnership is the evidence a caller supplies to prove it may
// retire a service-mode unit. Callers MUST capture it BEFORE teardown:
// once the tmux session is killed the server generation it belonged to is
// no longer observable.
type ServiceUnitOwnership struct {
	// SessionName is the tmux session name being retired. Empty means
	// "no tmux session" and always skips the stop.
	SessionName string

	// SocketName is the tmux `-L` socket of the server hosting
	// SessionName ("" = default socket, i.e. the shared one).
	SocketName string

	// RetiredServerPID is the pid of the tmux server generation the caller
	// retired. 0 when it could not be determined; a nonzero value lets the
	// gate distinguish "our generation is somehow still alive" from
	// "systemd already spawned a replacement generation".
	RetiredServerPID int

	// RetiredServerStartTime is field 22 from /proc/<pid>/stat. It binds the
	// PID to one process incarnation so PID reuse can never turn a stale
	// snapshot into ownership of an unrelated server.
	RetiredServerStartTime string

	// RetiredServerCgroup is the exact cgroup read from that PID before
	// teardown. It must exactly match the derived unit's ControlGroup before
	// the unit may be retired; a name-derived unit alone is never ownership.
	RetiredServerCgroup string
}

// ServiceUnitDecision reports what the ownership gate decided.
type ServiceUnitDecision struct {
	// Unit is the derived unit name the decision applies to.
	Unit string
	// Stopped is true only when `systemctl --user stop` was actually issued.
	Stopped bool
	// Reason is one of the UnitStop* constants above.
	Reason string
}

// serviceUnitState is the subset of `systemctl --user show` we need.
type serviceUnitState struct {
	ActiveState  string
	MainPID      int
	MainPIDAlive bool
	ControlGroup string
	KillMode     string
}

// serverProcessEvidence is the immutable identity and cgroup location of a
// tmux server process. It deliberately contains no executable/process-name
// heuristic: ownership comes only from the session's exact socket probe plus
// PID start time and cgroup membership.
type serverProcessEvidence struct {
	StartTime string
	Cgroup    string
}

// systemctlLookPath is a swappable seam so tests can simulate a host with
// (or without) systemctl regardless of the platform they run on.
var systemctlLookPath = func() error {
	_, err := exec.LookPath("systemctl")
	return err
}

// unitPIDAlive reports whether pid is still running (signal-0 probe: the
// kernel existence check with no delivery). Swappable for tests.
var unitPIDAlive = func(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// liveSessionsOnSocket is the ownership gate's view of server occupancy.
// A package var so tests can inject occupancy without a live tmux server;
// production always uses the bounded single `list-sessions` probe.
var liveSessionsOnSocket = ListSessionNamesOnSocket

// readServerProcessEvidence reads the cgroup-v2 location and immutable start
// time for pid. It is a seam so tests can deterministically model PID reuse
// and cgroup ownership without touching /proc.
var readServerProcessEvidence = func(pid int) (serverProcessEvidence, error) {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return serverProcessEvidence{}, err
	}
	closeParen := strings.LastIndex(string(stat), ")")
	if closeParen < 0 {
		return serverProcessEvidence{}, os.ErrInvalid
	}
	// Fields after the final ')' begin at proc field 3, so starttime (field
	// 22) is index 19. Parsing after the final ')' handles names containing
	// spaces or parentheses without relying on the process name as identity.
	fields := strings.Fields(string(stat[closeParen+1:]))
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex || fields[startTimeIndex] == "" {
		return serverProcessEvidence{}, os.ErrInvalid
	}

	cgroups, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
	if err != nil {
		return serverProcessEvidence{}, err
	}
	for _, line := range strings.Split(string(cgroups), "\n") {
		if _, path, ok := strings.Cut(strings.TrimSpace(line), "0::"); ok && path != "" {
			return serverProcessEvidence{StartTime: fields[startTimeIndex], Cgroup: path}, nil
		}
	}
	return serverProcessEvidence{}, os.ErrInvalid
}

// readServiceUnitState reads ActiveState + MainPID for one unit. Returns
// an error when systemctl cannot be consulted — an unknown unit is NOT an
// error (systemd reports ActiveState=inactive / MainPID=0 for not-found).
var readServiceUnitState = func(unit string) (serviceUnitState, error) {
	out, err := execCommand("systemctl", "--user", "show", unit,
		"-p", "ActiveState", "-p", "MainPID", "-p", "ControlGroup", "-p", "KillMode").Output()
	if err != nil {
		return serviceUnitState{}, err
	}
	return parseServiceUnitState(string(out)), nil
}

// parseServiceUnitState parses `systemctl show -p ActiveState -p MainPID`
// key=value output. Extracted so the parsing is testable without systemd.
func parseServiceUnitState(out string) serviceUnitState {
	var st serviceUnitState
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			st.ActiveState = strings.TrimSpace(value)
		case "MainPID":
			if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && n > 0 {
				st.MainPID = n
			}
		case "ControlGroup":
			st.ControlGroup = strings.TrimSpace(value)
		case "KillMode":
			st.KillMode = strings.ToLower(strings.TrimSpace(value))
		}
	}
	st.MainPIDAlive = unitPIDAlive(st.MainPID)
	return st
}

// unitStartJobInFlight reports whether systemd is bringing the unit up
// right now. In those states MainPID is not published yet, so a dead-pid
// reading must NOT be mistaken for "nothing left to protect".
func unitStartJobInFlight(activeState string) bool {
	switch strings.ToLower(strings.TrimSpace(activeState)) {
	case "activating", "reloading", "refreshing":
		return true
	}
	return false
}

// serviceUnitOwnershipProof verifies that the pre-teardown server identity
// belongs to this exact service cgroup. The socket is proven by ServerPID's
// tmux display-message probe; PID + starttime prevent PID reuse; cgroup binds
// that server to the unit. Missing evidence is intentionally a refusal.
func serviceUnitOwnershipProof(own ServiceUnitOwnership, state serviceUnitState) (bool, string) {
	if own.RetiredServerPID <= 0 || own.RetiredServerStartTime == "" || own.RetiredServerCgroup == "" ||
		state.ControlGroup == "" || own.RetiredServerCgroup != state.ControlGroup ||
		!strings.HasSuffix(state.ControlGroup, "/"+ServiceUnitName(own.SessionName)) {
		return false, UnitStopSkipOwnershipUnproven
	}
	if state.KillMode != "none" {
		// Units created before #2219 commonly report control-group here. Do
		// not stop them: another session can attach after our socket listing.
		return false, UnitStopSkipUnsafeKillMode
	}
	return true, UnitStopReasonExclusive
}

// evaluateServiceUnitOwnership is the pure ownership predicate: given the
// unit's observed state and the authoritative list of live sessions on the
// server, decide whether own.SessionName exclusively owns the unit.
//
// liveErr non-nil means the session probe was indeterminate; per
// ListSessionNamesOnSocket's contract that must be read as "assume alive",
// never as "no sessions".
func evaluateServiceUnitOwnership(
	own ServiceUnitOwnership,
	state serviceUnitState,
	live map[string]struct{},
	liveErr error,
) (bool, string) {
	if liveErr != nil {
		return false, UnitStopSkipIndeterminate
	}
	if _, stillThere := live[own.SessionName]; stillThere {
		return false, UnitStopSkipSessionStillLive
	}
	if len(live) > 0 {
		// Any remaining session — sibling agent-deck session OR one of the
		// user's own — shares the server this unit supervises.
		return false, UnitStopSkipServerShared
	}
	if unitStartJobInFlight(state.ActiveState) {
		return false, UnitStopSkipStartJobInFlight
	}
	if state.MainPIDAlive {
		if own.RetiredServerPID != 0 && state.MainPID != own.RetiredServerPID {
			return false, UnitStopSkipReplacementGeneration
		}
		return false, UnitStopSkipProcessAlive
	}
	return serviceUnitOwnershipProof(own, state)
}

// StopServiceUnitOwned stops + resets-failed the transient systemd-user
// service unit for own.SessionName ONLY when that session is proven to be
// its exclusive owner (issue #1721). It replaces the unconditional
// StopServiceUnit call the remove path used to make.
//
// Best-effort by design: every skip path and every non-systemd host is a
// no-op, never an error. The returned decision carries the reason so
// callers can log/report and tests can assert the gate.
func StopServiceUnitOwned(own ServiceUnitOwnership) ServiceUnitDecision {
	if strings.TrimSpace(own.SessionName) == "" {
		return ServiceUnitDecision{Reason: UnitStopSkipNoTmuxSession}
	}
	unit := ServiceUnitName(own.SessionName)
	if err := systemctlLookPath(); err != nil {
		return ServiceUnitDecision{Unit: unit, Reason: UnitStopSkipNoSystemctl}
	}

	state, err := readServiceUnitState(unit)
	if err != nil {
		statusLog.Warn("service_unit_stop_skipped",
			slog.String("unit", unit),
			slog.String("session", own.SessionName),
			slog.String("reason", UnitStopSkipIndeterminate),
			slog.String("error", err.Error()))
		return ServiceUnitDecision{Unit: unit, Reason: UnitStopSkipIndeterminate}
	}

	live, liveErr := liveSessionsOnSocket(own.SocketName)
	allowed, reason := evaluateServiceUnitOwnership(own, state, live, liveErr)
	if !allowed {
		statusLog.Info("service_unit_stop_skipped",
			slog.String("unit", unit),
			slog.String("session", own.SessionName),
			slog.String("reason", reason),
			slog.String("active_state", state.ActiveState),
			slog.Int("unit_main_pid", state.MainPID),
			slog.Int("retired_server_pid", own.RetiredServerPID),
			slog.Int("live_sessions_on_socket", len(live)))
		return ServiceUnitDecision{Unit: unit, Reason: reason}
	}

	// Exclusive owner: stop first (cancels Restart=on-failure), then clear
	// any failed state so the unit name is reusable. `stop` returns
	// non-zero for a unit that was never started — a no-op for us.
	_ = execCommand("systemctl", "--user", "stop", unit).Run()
	_ = execCommand("systemctl", "--user", "reset-failed", unit).Run()

	statusLog.Info("service_unit_stopped",
		slog.String("unit", unit),
		slog.String("session", own.SessionName),
		slog.String("reason", reason),
		slog.Int("retired_server_pid", own.RetiredServerPID))

	return ServiceUnitDecision{Unit: unit, Stopped: true, Reason: reason}
}

// ServiceUnitOwnership snapshots the ownership evidence for this session's
// service unit. MUST be called while the session is still live — it reads
// the pid of the tmux server generation the session belongs to, which is
// unobservable after teardown.
func (s *Session) ServiceUnitOwnership() ServiceUnitOwnership {
	if s == nil {
		return ServiceUnitOwnership{}
	}
	own := ServiceUnitOwnership{SessionName: s.Name, SocketName: s.SocketName}
	pid := s.ServerPID() // exact Session.SocketName probe; no name matching.
	if pid <= 0 {
		return own
	}

	first, err := readServerProcessEvidence(pid)
	if err != nil {
		return own
	}
	// Re-read the server PID and its immutable evidence. If it changed while
	// snapshotting, PID reuse or server replacement made ownership unprovable.
	if s.ServerPID() != pid {
		return own
	}
	second, err := readServerProcessEvidence(pid)
	if err != nil || first != second {
		return own
	}
	own.RetiredServerPID = pid
	own.RetiredServerStartTime = first.StartTime
	own.RetiredServerCgroup = first.Cgroup
	return own
}

// ServerPID returns the pid of the tmux server hosting this session, or 0
// when it cannot be determined (server gone, wedged, or probe timeout).
// Bounded like every other read-only probe in this package.
func (s *Session) ServerPID() int {
	out, err := s.runBoundedOutput("display-message", "-t", s.Name, "-p", "#{pid}")
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0
	}
	return pid
}
