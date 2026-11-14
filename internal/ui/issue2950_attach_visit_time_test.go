package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// #2950 review item 1: opening a waiting session acks it, and the in-memory
// visit time must move with the ack. Otherwise the detach refresh does a cold
// hook read against the visit time from load, finds the Stop newer than it,
// and turns the row back to waiting until the next reload.
func TestIssue2950_AttachAckRecordsVisitTime(t *testing.T) {
	h := blackscreenTestHome(t)
	inst := session.NewInstanceWithTool("ack-attach", t.TempDir(), "shell")
	inst.Status = session.StatusWaiting
	h.instances = append(h.instances, inst)
	h.instanceByID[inst.ID] = inst

	before := time.Now()
	_ = h.attachSession(inst)

	if !inst.GetTmuxSession().IsAcknowledged() {
		t.Fatal("precondition: attaching a waiting session acknowledges it")
	}
	if got := inst.AcknowledgedAt(); got.Before(before) {
		t.Fatalf("AcknowledgedAt after the attach ack = %v, want >= %v: the visit time stayed at its load value", got, before)
	}
}
