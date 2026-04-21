package session

import (
	"testing"
)

// TestReviver_Classify_ThreadsSocketNameToTmuxExists is the contract for
// issue #687 phase 1 reviver plumbing (v1.7.50). The reviver's TmuxExists
// callback must receive the instance's stored socket name, not just the
// session name — otherwise revive scans would ask the user's default
// tmux server about sessions living on the agent-deck isolated socket
// and wrongly classify them as dead.
func TestReviver_Classify_ThreadsSocketNameToTmuxExists(t *testing.T) {
	var observedName, observedSocket string

	r := &Reviver{
		TmuxExists: func(name, socketName string) bool {
			observedName = name
			observedSocket = socketName
			// Pretend the session exists on the isolated socket.
			return socketName == "agent-deck"
		},
		PipeAlive:    func(string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
	}

	inst := &Instance{
		ID:             "sock-probe-1",
		Title:          "isolated-session",
		ProjectPath:    "/tmp",
		Tool:           "shell",
		Status:         StatusRunning,
		TmuxSocketName: "agent-deck",
	}

	class := r.Classify(inst)
	if class != ClassAlive {
		t.Fatalf("Classify must report alive when tmux probe says so on the right socket; got %v", class)
	}
	if observedSocket != "agent-deck" {
		t.Fatalf("TmuxExists did not receive instance socket name; got %q want %q", observedSocket, "agent-deck")
	}
	if observedName == "" {
		t.Fatalf("TmuxExists must still receive the tmux session name (got empty)")
	}
}

// TestReviver_Classify_DefaultSocketSessionsUseEmptyString pins the
// backward-compat path: a pre-v1.7.50 Instance with no TmuxSocketName
// must probe the default server (empty socket string), not accidentally
// invent a name.
func TestReviver_Classify_DefaultSocketSessionsUseEmptyString(t *testing.T) {
	var observedSocket string

	r := &Reviver{
		TmuxExists: func(_, socketName string) bool {
			observedSocket = socketName
			return true
		},
		PipeAlive:    func(string) bool { return true },
		ReviveAction: func(*Instance) error { return nil },
	}

	inst := &Instance{
		ID:          "legacy-probe-1",
		Title:       "default-server-session",
		ProjectPath: "/tmp",
		Tool:        "shell",
		Status:      StatusRunning,
		// TmuxSocketName intentionally empty — legacy row shape.
	}

	_ = r.Classify(inst)

	if observedSocket != "" {
		t.Fatalf("Legacy Instance with no TmuxSocketName must yield empty socket string to probe; got %q", observedSocket)
	}
}

// TestReviver_ReviveAction_ThreadsSocketToPipeConnect is a smoke-level
// check that the ReviveAction path also consumes Instance.TmuxSocketName.
// Without this, the pipe reconnection during a revive cycle would target
// the default server even for sessions that live elsewhere, creating
// exactly the orphan-pipe class of bug #677 was introduced to kill.
//
// We exercise defaultReviveAction indirectly by swapping it for a stub
// that records the instance it receives — the real Connect wiring is
// covered by TestReviver_ThreadsSocketInPipemanagerFlow in
// internal/tmux/controlpipe_test.go (not yet — tracked as follow-up).
func TestReviver_ReviveAction_ReceivesInstanceWithSocket(t *testing.T) {
	var seenSocket string

	r := &Reviver{
		TmuxExists: func(string, string) bool { return true },
		PipeAlive:  func(string) bool { return false }, // force errored → action runs
		ReviveAction: func(inst *Instance) error {
			seenSocket = inst.TmuxSocketName
			return nil
		},
	}

	inst := &Instance{
		ID:             "revive-sock-1",
		Title:          "needs-revive",
		ProjectPath:    "/tmp",
		Tool:           "shell",
		Status:         StatusError,
		TmuxSocketName: "agent-deck",
	}

	outcome := r.ReviveOne(inst)
	if !outcome.Revived {
		t.Fatalf("expected ReviveOne to run the action (outcome=%+v)", outcome)
	}
	if seenSocket != "agent-deck" {
		t.Fatalf("ReviveAction did not see instance socket name; got %q want %q", seenSocket, "agent-deck")
	}
}
