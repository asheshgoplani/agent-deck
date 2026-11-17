package session

import (
	"strings"
	"testing"
	"time"
)

// Issue #3165: a worker launched from inside a sub-session (a coordinator
// under a root) reports to that coordinator. Its records land in the
// coordinator's inbox, not the root's, on both launch paths: top-level
// (default) and nested under the root ([launch] nest_under_parent).

func issue3165Fleet() (root, coord *Instance) {
	root = conductorParent("root-3165")
	root.Title = "root-session"
	coord = &Instance{
		ID: "coord-3165", Title: "coordinator", ProjectPath: "/tmp/coord", GroupPath: DefaultGroupPath,
		ParentSessionID: root.ID, Tool: "claude", Status: StatusIdle, CreatedAt: time.Now(),
	}
	return root, coord
}

func issue3165Worker(id, parent, reportsTo string) *Instance {
	w := &Instance{
		ID: id, Title: "worker-" + id, ProjectPath: "/tmp/w", GroupPath: DefaultGroupPath,
		ParentSessionID: parent, Tool: "claude", Status: StatusWaiting, CreatedAt: time.Now(),
	}
	w.SetReportsTo(reportsTo)
	return w
}

func TestIssue3165_WorkerSentinelReachesTheCoordinatorNotTheRoot(t *testing.T) {
	for _, tc := range []struct {
		name, profile string
		parent        string // worker's tree parent
	}{
		{"default: worker top-level", "_test-3165-top", ""},
		{"nest_under_parent: worker under the root", "_test-3165-nested", "root-3165"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, coord := issue3165Fleet()
			worker := issue3165Worker("wrk-3165", tc.parent, coord.ID)
			profile := tc.profile
			seedNotifyInstances(t, profile, root, coord, worker)

			n := NewTransitionNotifier()
			t.Cleanup(n.Close)
			res := n.NotifyFinished(TransitionNotificationEvent{
				ChildSessionID: worker.ID, ChildTitle: worker.Title, Profile: profile,
				DoneStatus: "ok", DoneSummary: "printed the done line", Timestamp: time.Now(),
				// The kickoff carried the coordinator's envelope.
				FromID: coord.ID, Trigger: TurnTriggerSend, Tier: TurnTierUrgent,
			})
			if res.DeliveryResult != transitionDeliveryCommitted {
				t.Fatalf("delivery = %q (reason %q), want committed", res.DeliveryResult, res.DeadLetterReason)
			}
			got := readInboxLines(t, coord.ID)
			if len(got) != 1 || got[0].ChildSessionID != worker.ID || got[0].TargetSessionID != coord.ID {
				t.Fatalf("coordinator inbox = %+v, want the worker's one record", got)
			}
			if rootGot := readInboxLines(t, root.ID); len(rootGot) != 0 {
				t.Fatalf("root inbox = %+v, want no copy", rootGot)
			}
		})
	}
}

// The wake gate is unchanged; only the target is. A record that answers the
// coordinator's tagged kickoff is a reply wake for the coordinator.
func TestIssue3165_TaggedKickoffAnswerWakesTheCoordinatorAsAReply(t *testing.T) {
	_, coord := issue3165Fleet()
	worker := issue3165Worker("wrk-3165", "", coord.ID)
	byID := map[string]*Instance{coord.ID: coord, worker.ID: worker}
	target := resolveParentNotificationTarget(worker, byID)
	if target != coord {
		t.Fatalf("notification target = %v, want the coordinator", target)
	}
	ev := parentWakeEvent(TransitionNotificationEvent{FromID: coord.ID, Trigger: TurnTriggerSend}, target)
	if ev.TargetKind != InboxTargetKindReply || !isWakeTarget(target, ev.TargetKind) {
		t.Fatalf("kind %q wake=%v, want a reply wake", ev.TargetKind, isWakeTarget(target, ev.TargetKind))
	}
	if s := replySenderFor(TransitionNotificationEvent{FromID: coord.ID, Trigger: TurnTriggerSend}, worker, target, byID); s != nil {
		t.Fatalf("reply sender %s would duplicate the coordinator's record", s.ID)
	}
}

// The root still gets its tagged-send reply from a nested worker it messaged:
// it no longer holds the worker's record, so the reply copy is its only one.
func TestIssue3165_RootSendToNestedWorkerStillGetsAReply(t *testing.T) {
	root, coord := issue3165Fleet()
	worker := issue3165Worker("wrk-3165", root.ID, coord.ID)
	byID := map[string]*Instance{root.ID: root, coord.ID: coord, worker.ID: worker}
	ev := TransitionNotificationEvent{FromID: root.ID, Trigger: TurnTriggerSend}
	if s := replySenderFor(ev, worker, coord, byID); s != root {
		t.Fatalf("reply sender = %v, want the root", s)
	}
}

func TestIssue3165_ReportTargetID(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		parent, reportsTo, w string
	}{
		{"plain sub-session", "p", "", "p"},
		{"top-level", "", "", ""},
		{"top-level worker", "", "c", "c"},
		{"nested worker", "p", "c", "c"},
		{"self-pointing launcher ignored", "p", "self", "p"},
	} {
		inst := &Instance{ID: "self", ParentSessionID: tc.parent, ReportsTo: tc.reportsTo}
		if got := inst.ReportTargetID(); got != tc.w {
			t.Errorf("%s: ReportTargetID = %q, want %q", tc.name, got, tc.w)
		}
	}
	if (*Instance)(nil).ReportTargetID() != "" {
		t.Error("nil instance has a report target")
	}
	if got := statsParentFor(&Instance{ID: "w", ReportsTo: "c"}); got != "c" {
		t.Errorf("stats bucket = %q, want the launcher", got)
	}
}

// reports_to survives a save and load, and an explicit clear survives too
// (the extras merge would otherwise carry the stored value forward).
func TestIssue3165_ReportsToPersistsAndClears(t *testing.T) {
	root, coord := issue3165Fleet()
	worker := issue3165Worker("wrk-3165", "", coord.ID)
	profile := "_test-3165-persist"
	seedNotifyInstances(t, profile, root, coord, worker)

	loadAll := func() ([]*Instance, *Instance) {
		t.Helper()
		st, err := NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		insts, _, err := st.LoadWithGroups()
		if err != nil {
			t.Fatal(err)
		}
		for _, i := range insts {
			if i.ID == worker.ID {
				return insts, i
			}
		}
		t.Fatal("worker not stored")
		return nil, nil
	}
	save := func(insts []*Instance) {
		t.Helper()
		st, err := NewStorageWithProfile(profile)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := st.SaveWithGroups(insts, nil); err != nil {
			t.Fatal(err)
		}
	}
	all, got := loadAll()
	if got.ReportsTo != coord.ID {
		t.Fatalf("loaded reports_to = %q, want %q", got.ReportsTo, coord.ID)
	}

	// A save from a copy that does not carry the field keeps the stored value.
	got.ReportsTo = ""
	save(all)
	if _, after := loadAll(); after.ReportsTo != coord.ID {
		t.Fatalf("after an unaware save reports_to = %q, want it kept", after.ReportsTo)
	}

	all, got = loadAll()
	got.ClearReportsTo()
	save(all)
	if _, after := loadAll(); after.ReportsTo != "" {
		t.Fatalf("after ClearReportsTo reports_to = %q, want empty", after.ReportsTo)
	}
}

// The identity block names the launcher, and a top-level worker is not told
// it is a root session.
func TestIssue3165_IdentityBlockNamesTheLauncher(t *testing.T) {
	worker := issue3165Worker("wrk-3165", "", "coord-3165")
	block := worker.BuildIdentityPrompt()
	if !strings.Contains(block, "- reports to: coord-3165") {
		t.Fatalf("identity block lacks the reports-to line:\n%s", block)
	}
	if strings.Contains(block, "this is a root session") {
		t.Fatalf("a worker that reports to a launcher is called a root session:\n%s", block)
	}
	plain := &Instance{ID: "x", Title: "x", Tool: "claude"}
	if b := plain.BuildIdentityPrompt(); strings.Contains(b, "reports to") {
		t.Fatalf("a session without a launcher has a reports-to line:\n%s", b)
	}
}
