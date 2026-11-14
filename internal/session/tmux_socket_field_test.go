package session

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func stubTmuxSessionRunning(t *testing.T, running bool) {
	t.Helper()
	prev := tmuxSessionRunning
	tmuxSessionRunning = func(*Instance) bool { return running }
	t.Cleanup(func() { tmuxSessionRunning = prev })
}

// A stopped session moves to another tmux socket: the stored socket and the
// tmux handle both change, so the next start creates it there.
func TestSetField_TmuxSocket_MovesStoppedSession(t *testing.T) {
	stubTmuxSessionRunning(t, false)
	inst := &Instance{ID: "w1", tmuxSession: &tmux.Session{Name: "agentdeck_w1"}}

	old, _, err := SetField(inst, FieldTmuxSocket, " deck36 ", nil)
	if err != nil {
		t.Fatalf("SetField: %v", err)
	}
	if old != "" || inst.TmuxSocketName != "deck36" || inst.GetTmuxSession().SocketName != "deck36" {
		t.Fatalf("old=%q stored=%q handle=%q, want \"\" deck36 deck36", old, inst.TmuxSocketName, inst.GetTmuxSession().SocketName)
	}

	// '' moves it back to the default server.
	if _, _, err := SetField(inst, FieldTmuxSocket, "", nil); err != nil || inst.TmuxSocketName != "" || inst.GetTmuxSession().SocketName != "" {
		t.Fatalf("back to default: err=%v stored=%q", err, inst.TmuxSocketName)
	}
}

// A running session is refused: its pane would be stranded on the old server.
func TestSetField_TmuxSocket_RefusedWhileRunning(t *testing.T) {
	stubTmuxSessionRunning(t, true)
	inst := &Instance{ID: "w2", TmuxSocketName: "old", tmuxSession: &tmux.Session{Name: "agentdeck_w2", SocketName: "old"}}

	_, _, err := SetField(inst, FieldTmuxSocket, "deck36", nil)
	var me *MutationError
	if !errors.As(err, &me) {
		t.Fatalf("want MutationError, got %v", err)
	}
	if inst.TmuxSocketName != "old" || inst.GetTmuxSession().SocketName != "old" {
		t.Fatalf("socket changed on refusal: %q / %q", inst.TmuxSocketName, inst.GetTmuxSession().SocketName)
	}
	// Setting the socket it already has is a no-op, not a refusal.
	if _, _, err := SetField(inst, FieldTmuxSocket, "old", nil); err != nil {
		t.Fatalf("same socket: %v", err)
	}
}

func TestSetField_TmuxSocket_RejectsPathsAndJunk(t *testing.T) {
	stubTmuxSessionRunning(t, false)
	for _, bad := range []string{"../x", "/tmp/sock", "a b", "-L", "x;y"} {
		inst := &Instance{ID: "w3"}
		if _, _, err := SetField(inst, FieldTmuxSocket, bad, nil); err == nil {
			t.Errorf("socket %q accepted", bad)
		}
		if inst.TmuxSocketName != "" {
			t.Errorf("socket %q stored", bad)
		}
	}
}

func TestValidMutableFields_IncludesTmuxSocket(t *testing.T) {
	for _, f := range ValidMutableFields {
		if f == FieldTmuxSocket {
			return
		}
	}
	t.Errorf("FieldTmuxSocket missing from ValidMutableFields")
}
