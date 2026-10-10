package session

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/stretchr/testify/require"
)

type recordingAppender struct {
	mu     sync.Mutex
	events []health.Event
}

func (r *recordingAppender) Append(e health.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *recordingAppender) snapshot() []health.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]health.Event(nil), r.events...)
}

// The session status event (events follow) carries exit_code for an observed
// custom-command exit, and the transition notification event does too.
func TestExitStatus1628_StatusEventAndTransitionCarryExitCode(t *testing.T) {
	inst := NewInstance("exit-event", t.TempDir())
	inst.Tool, inst.Command, inst.TrackCommandExit = "shell", "exit 3", true
	inst.Status = StatusError
	code := 3
	inst.terminatedPaneSubstate = SubstateProcessExited
	inst.terminatedPaneExitCode = &code

	rec := &recordingAppender{}
	d := NewTransitionDaemon()
	writer := health.NewAsyncWriter(rec, 8)
	d.journalWriters["p"] = writer
	byID := map[string]*Instance{inst.ID: inst}
	d.journalStatusChanges("p", byID, map[string]string{inst.ID: "running"}, map[string]string{})
	d.journalStatusChanges("p", byID, map[string]string{inst.ID: "error"}, map[string]string{inst.ID: string(SubstateProcessExited)})
	writer.Stop(2 * time.Second)

	events := rec.snapshot()
	require.Len(t, events, 1)
	require.Equal(t, "error", events[0].To)
	require.Equal(t, 3, events[0].Detail["exit_code"])
	require.Equal(t, string(SubstateProcessExited), events[0].Detail["substate"])

	ev := TransitionNotificationEvent{ExitCode: inst.ExitCode()}
	raw, err := json.Marshal(ev)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"exit_code":3`)
}

// An interactive shell (no tracked command) never reports process-exited or
// an exit code, so older shell sessions keep their behavior.
func TestExitStatus1628_UntrackedShellHasNoExitCode(t *testing.T) {
	inst := NewInstance("plain-shell", t.TempDir())
	inst.Tool, inst.Command = "shell", "htop"
	status, substate := inst.classifyCustomCommandExit(StatusError, SubstateNone, 3, true)
	require.Equal(t, StatusError, status)
	require.Equal(t, SubstateNone, substate)
	inst.setTerminatedPaneExitCode(3, true)
	require.Nil(t, inst.terminatedPaneExitCode)
	require.Nil(t, (*Instance)(nil).ExitCode())
}
