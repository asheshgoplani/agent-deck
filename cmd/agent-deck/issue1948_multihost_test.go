package main

// Issue #1948, review round 2 — the BLOCKING finding.
//
// A child id is a caller-chosen string (`run-task --child <ID>` accepts
// anything), so two hosts running the same named task mint the SAME id. Before
// this feature those ids never left the host that minted them; the drain is
// what makes them cross. Everything downstream keys record identity on the
// child id — collapseLastWins, TurnFingerprint, EventFingerprint — so without
// namespacing, one host's record silently destroys the other's while the drain
// reports "1 new".
//
// The reviewer reproduced both shapes over real ssh. These pin both.

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// twoHostFetcher answers with a different record per remote, both carrying the
// SAME child id — the collision the fleet produces naturally.
func twoHostFetcher(child string, at time.Time) remoteRecordFetcher {
	return func(_ context.Context, name string, _ session.RemoteConfig) ([]session.TransitionNotificationEvent, error) {
		outcome := map[string]string{"boxb": "ok", "boxc": "fail"}[name]
		return []session.TransitionNotificationEvent{{
			ChildSessionID: child,
			ChildTitle:     "nightly-build",
			Profile:        "default",
			Kind:           "finished",
			DoneStatus:     outcome,
			DoneSummary:    "nightly-build on " + name,
			Timestamp:      at,
		}}, nil
	}
}

// Shape 1: both hosts drained into one inbox in the same window. Both
// completions must reach the conductor — the ok AND the fail.
func TestIssue1948R2_TwoHostsSameChildID_BothSurviveOneDrainWindow(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	configureRemote(t, "boxc", "worker@box-c")
	conductor := "conductor-1948-multihost"
	registerDrainTarget(t, conductor)

	fetch := twoHostFetcher("nightly-build", time.Now())
	for _, remote := range []string{"boxb", "boxc"} {
		var stdout, stderr bytes.Buffer
		if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, remote}, fetch); code != 0 {
			t.Fatalf("%s drain exit=%d: %s", remote, code, stderr.String())
		}
	}

	pending, err := session.ReadInboxEvents(conductor)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(pending) != 2 {
		t.Fatalf("two hosts' completions must both land, inbox holds %d: %+v", len(pending), pending)
	}

	// And the conductor's own consumption path must SHOW both. This is where
	// the loss happened: collapseLastWins keys on the child id, so two records
	// sharing one id collapse to the last-seen.
	var drained bytes.Buffer
	if err := runInbox(&drained, []string{"drain", conductor}); err != nil {
		t.Fatalf("inbox drain: %v", err)
	}
	out := drained.String()
	if !strings.Contains(out, "boxb") || !strings.Contains(out, "boxc") {
		t.Fatalf("a completion was destroyed by the other host's record:\n%s", out)
	}
	if !strings.Contains(out, "finished=ok") || !strings.Contains(out, "finished=fail") {
		t.Fatalf("both outcomes must survive; got:\n%s", out)
	}
}

// sameOutcomeFetcher is the commoner and worse collision: two hosts running the
// same named task that both SUCCEED, so the records agree on child id, status
// and summary. TurnFingerprint is child + status + summary, so the second host's
// record hashes to a turn the conductor has already consumed.
func sameOutcomeFetcher(child string, at time.Time) remoteRecordFetcher {
	return func(_ context.Context, name string, _ session.RemoteConfig) ([]session.TransitionNotificationEvent, error) {
		return []session.TransitionNotificationEvent{{
			ChildSessionID: child,
			ChildTitle:     "nightly-build",
			Profile:        "default",
			Kind:           "finished",
			DoneStatus:     "ok",
			DoneSummary:    "nightly-build finished",
			Timestamp:      at,
		}}, nil
	}
}

// Shape 2, the worse one: drain box-b, CONSUME it, then drain box-c. The
// consumed-turn ledger must not treat box-c's record as already delivered —
// the drain says "1 new" and the next heartbeat must not say "No pending".
func TestIssue1948R2_TwoHostsSameChildID_SurviveSequentialDrains(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	configureRemote(t, "boxc", "worker@box-c")
	conductor := "conductor-1948-sequential"
	registerDrainTarget(t, conductor)

	fetch := sameOutcomeFetcher("nightly-build", time.Now())

	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("boxb drain exit=%d: %s", code, stderr.String())
	}
	var firstHeartbeat bytes.Buffer
	if err := runInbox(&firstHeartbeat, []string{"drain", conductor}); err != nil {
		t.Fatalf("heartbeat 1: %v", err)
	}
	if !strings.Contains(firstHeartbeat.String(), "boxb:nightly-build") {
		t.Fatalf("box-b's completion missing from heartbeat 1:\n%s", firstHeartbeat.String())
	}

	stdout.Reset()
	if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxc"}, fetch); code != 0 {
		t.Fatalf("boxc drain exit=%d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "1 new") {
		t.Fatalf("box-c's record should be new to this inbox:\n%s", stdout.String())
	}

	var secondHeartbeat bytes.Buffer
	if err := runInbox(&secondHeartbeat, []string{"drain", conductor}); err != nil {
		t.Fatalf("heartbeat 2: %v", err)
	}
	out := secondHeartbeat.String()
	if strings.Contains(out, "No pending events") {
		t.Fatalf("the drain reported '1 new' and the next heartbeat saw nothing — box-c's completion was destroyed:\n%s", out)
	}
	if !strings.Contains(out, "boxc:nightly-build") {
		t.Fatalf("box-c's completion must reach the conductor:\n%s", out)
	}
}

// The stored id names its host, in the repo's existing `<remote>:<session>`
// spelling, and re-draining does not stack prefixes.
func TestIssue1948R2_StoredChildIDNamesItsHostAndDoesNotStack(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-scoped"
	registerDrainTarget(t, conductor)

	fetch := twoHostFetcher("nightly-build", time.Now())
	var stdout, stderr bytes.Buffer
	for i := 0; i < 2; i++ {
		stdout.Reset()
		if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
			t.Fatalf("drain exit=%d: %s", code, stderr.String())
		}
	}

	pending, err := session.ReadInboxEvents(conductor)
	if err != nil {
		t.Fatalf("read inbox: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("re-draining one remote must stay idempotent, got %d: %+v", len(pending), pending)
	}
	if pending[0].ChildSessionID != "boxb:nightly-build" {
		t.Fatalf("stored id must name its host once, got %q", pending[0].ChildSessionID)
	}
}

// A conductor that drains a host holding a LOCAL child id equal to one of its
// own children must not have the two collide either.
func TestIssue1948R2_RemoteChildDoesNotCollideWithLocalChild(t *testing.T) {
	drainTestHome(t)
	configureRemote(t, "boxb", "worker@box-b")
	conductor := "conductor-1948-localclash"
	registerDrainTarget(t, conductor)

	// A local child of this conductor, committed the ordinary way.
	if err := session.CommitToInbox(conductor, session.TransitionNotificationEvent{
		ChildSessionID: "nightly-build", ChildTitle: "local nightly", Profile: "default",
		Kind: "finished", DoneStatus: "ok", DoneSummary: "local run", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit local: %v", err)
	}

	fetch := twoHostFetcher("nightly-build", time.Now())
	var stdout, stderr bytes.Buffer
	if code := runRemoteDrain(&stdout, &stderr, []string{"--into", conductor, "boxb"}, fetch); code != 0 {
		t.Fatalf("drain exit=%d: %s", code, stderr.String())
	}

	var drained bytes.Buffer
	if err := runInbox(&drained, []string{"drain", conductor}); err != nil {
		t.Fatalf("inbox drain: %v", err)
	}
	// Both must be present and distinguishable: the local child under its bare
	// id, the pulled one under its host-scoped id.
	out := drained.String()
	if !strings.Contains(out, "child=nightly-build ") {
		t.Fatalf("the conductor's own local child record was destroyed by a remote one:\n%s", out)
	}
	if !strings.Contains(out, "child=boxb:nightly-build ") {
		t.Fatalf("the pulled record was destroyed by the local one:\n%s", out)
	}
}
