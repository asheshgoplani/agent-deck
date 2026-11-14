package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The Edit Session dialog carries the per-session wait timer for Claude
// sessions (ASK-119), as a live field.
func TestEditSessionDialog_WaitTimeoutField(t *testing.T) {
	d := NewEditSessionDialog()
	inst := sampleInstance()
	inst.WaitTimeout = "5m"
	d.Show(inst)

	idx := -1
	for i, f := range d.fields {
		if f.key == session.FieldWaitTimeout {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatal("Claude session has no wait-timeout field")
	}
	if got := d.fields[idx].input.Value(); got != "5m" {
		t.Fatalf("field shows %q, want the session's 5m", got)
	}
	if !strings.Contains(d.View(), "Wait timer") {
		t.Errorf("View() lacks the wait timer row:\n%s", d.View())
	}
	if len(d.GetChanges(inst)) != 0 {
		t.Fatalf("unchanged dialog reports changes: %+v", d.GetChanges(inst))
	}
	d.fields[idx].input.SetValue("off")
	changes := d.GetChanges(inst)
	if len(changes) != 1 || changes[0].Field != session.FieldWaitTimeout || changes[0].Value != "off" || !changes[0].IsLive {
		t.Fatalf("changes = %+v, want one live wait-timeout=off", changes)
	}

	shell := sampleInstance()
	shell.Tool = "shell"
	d.Show(shell)
	for _, f := range d.fields {
		if f.key == session.FieldWaitTimeout {
			t.Fatal("a non-Claude session must not offer the wait timer")
		}
	}
}

// Only the primary TUI, never a headless web server, ticks the wait timer,
// so two processes never press a key at the same prompt; with no session
// armed it does not even run the election.
func TestHome_TickWaitTimerOnlyAsPrimary(t *testing.T) {
	inst := sampleInstance()
	inst.Status = session.StatusWaiting
	inst.WaitTimeout = "5m"
	captures := 0
	timer := session.NewWaitTimer(session.WaitTimerConfig{
		Now:            func() time.Time { return time.Unix(0, 0) },
		Capture:        func(*session.Instance) (string, error) { captures++; return "", nil },
		Attached:       func(*session.Instance) (bool, error) { return false, nil },
		SendKey:        func(*session.Instance, string) error { t.Fatal("no key expected"); return nil },
		LogEvent:       func(session.SessionLifecycleEvent) error { return nil },
		DefaultTimeout: func() time.Duration { return 0 },
	})
	elections := 0
	primary := false
	orig := waitTimerPrimary
	waitTimerPrimary = func() bool { elections++; return primary }
	t.Cleanup(func() { waitTimerPrimary = orig })

	h := &Home{waitTimer: timer}
	h.tickWaitTimer([]*session.Instance{inst})
	if captures != 0 || elections != 1 {
		t.Fatalf("non-primary: captures=%d elections=%d", captures, elections)
	}
	h.headless = true
	primary = true
	h.tickWaitTimer([]*session.Instance{inst})
	if captures != 0 {
		t.Fatalf("headless ticked the wait timer: captures=%d", captures)
	}
	h.headless = false
	h.tickWaitTimer([]*session.Instance{inst})
	if captures != 1 {
		t.Fatalf("primary did not tick: captures=%d", captures)
	}
	inst.WaitTimeout = ""
	elections = 0
	h.tickWaitTimer([]*session.Instance{inst})
	if elections != 0 || captures != 1 {
		t.Fatalf("unarmed sweep ran the election or read a pane: elections=%d captures=%d", elections, captures)
	}
}
