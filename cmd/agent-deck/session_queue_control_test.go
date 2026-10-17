package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
)

// Worker side of `session queue release|cancel` (CORE-CHANGES 22), ported
// from the lab core together with its later race fixes.

func queueControlFixture(t *testing.T, state string) (string, *sendqueue.Record) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now().UTC()
	rec := &sendqueue.Record{
		SendID: sendqueue.NewID(now), State: state, Verdict: "queued",
		SessionID: "queue-control-target", Message: "unique queue control text",
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	return dir, rec
}

func queueControlResultForTest(t *testing.T, dir, id string) queueControlResult {
	t.Helper()
	b, err := os.ReadFile(sendqueue.ControlResultPath(dir, id))
	if err != nil {
		t.Fatal(err)
	}
	var result queueControlResult
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("decode control result: %v: %s", err, b)
	}
	return result
}

// A control call that meets an entry the normal dequeue is typing waits for
// the child's evidence instead of answering already_sent from the state alone.
func TestQueueControlWaitsForTypingEvidence(t *testing.T) {
	bin := channelsCLIBinary(t)
	for _, operation := range []string{"release", "cancel"} {
		t.Run(operation, func(t *testing.T) {
			home := t.TempDir()
			dir := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "sendqueue")
			id := sendqueue.NewID(time.Now())
			rec := &sendqueue.Record{SendID: id, SessionID: "typing-target", State: sendqueue.StateTyping, Attempts: 1, SentAt: time.Now().UTC().Format(time.RFC3339Nano)}
			if err := sendqueue.Save(dir, rec); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "session", "queue", operation, id, "--json")
			cmd.Env = agentDeckTestEnv(home, nil)
			var output bytes.Buffer
			cmd.Stdout = &output
			cmd.Stderr = &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(300 * time.Millisecond)
			if _, err := sendqueue.Update(dir, id, time.Now(), func(r *sendqueue.Record) {
				r.State = sendqueue.StateSubmitted
				r.DeliveryEvidence = map[string]interface{}{"delivery": "delivered", "confirmation": "confirmed", "submitted": true}
			}); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("command failed: %v: %s", err, output.String())
			}
			var got struct {
				Outcome          string                 `json:"outcome"`
				DeliveryEvidence map[string]interface{} `json:"delivery_evidence"`
			}
			if err := json.Unmarshal(output.Bytes(), &got); err != nil || got.Outcome != "already_sent" || got.DeliveryEvidence["confirmation"] != "confirmed" {
				t.Fatalf("unconfirmed typing race: %v: %+v output=%s", err, got, output.String())
			}
			if _, err := os.Stat(sendqueue.ControlPath(dir, id)); !os.IsNotExist(err) {
				t.Fatalf("a typing entry must not get a control request: %v", err)
			}
		})
	}
}

func TestQueueControlServicesLaterCancelWhileFirstEntryWaits(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "0")
	dir, first := queueControlFixture(t, sendqueue.StateQueued)
	second := *first
	second.SendID = sendqueue.NewID(time.Now().Add(time.Second))
	second.Message = "cancel this later entry"
	if err := sendqueue.Save(dir, &second); err != nil {
		t.Fatal(err)
	}
	if err := requestQueueControl(dir, second.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if !queueControlPending(dir, first.SessionID) {
		t.Fatal("pending control request not seen")
	}
	serviceOtherQueueControls("", dir, first.SessionID, first.SendID)
	got, err := sendqueue.Load(dir, second.SendID)
	if err != nil || got.State != sendqueue.StateCancelled || got.Attempts != 0 {
		t.Fatalf("later cancel waited behind first entry: %+v %v", got, err)
	}
	if result := queueControlResultForTest(t, dir, second.SendID); result.Outcome != "cancelled" {
		t.Fatalf("cancel result: %+v", result)
	}
	if queueControlPending(dir, first.SessionID) {
		t.Fatal("answered control request still reported pending")
	}
	if first, err := sendqueue.Load(dir, first.SendID); err != nil || first.State != sendqueue.StateQueued {
		t.Fatalf("first entry changed: %+v %v", first, err)
	}
}

func TestQueueReleaseDoesNotPromoteUnconfirmedTyping(t *testing.T) {
	rec := &sendqueue.Record{State: sendqueue.StateTyped, Verdict: "delivered", DeliveryEvidence: map[string]interface{}{
		"delivery": "delivered", "confirmation": "unknown", "submitted": false,
	}}
	if got := queueReleaseResult(rec); got.Outcome != "unconfirmed" || got.Record.DeliveryEvidence["confirmation"] != "unknown" {
		t.Fatalf("typed receipt was promoted to confirmed delivery: %+v", got)
	}
	for _, state := range []string{sendqueue.StateSubmitted, sendqueue.StateLanded} {
		rec.State = state
		if got := queueReleaseResult(rec); got.Outcome != "delivered" {
			t.Fatalf("%s lost: %+v", state, got)
		}
	}
	// Refused before typing, also after the #2549 refusal limit failed it.
	rec.State, rec.Reason = sendqueue.StateQueued, "retrying: composer_blocked"
	if got := queueReleaseResult(rec); got.Outcome != "refused" || got.Reason != "composer_blocked" {
		t.Fatalf("queued after release: %+v", got)
	}
	rec.State, rec.Reason = sendqueue.StateFailed, "not delivered: the Codex session identity stayed unavailable"
	if got := queueReleaseResult(rec); got.Outcome != "refused" || !strings.Contains(got.Reason, "not delivered") {
		t.Fatalf("failed after release: %+v", got)
	}
}

func TestQueueControlCancelPersistsBeforeTyping(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "0")
	dir, rec := queueControlFixture(t, sendqueue.StateQueued)
	if err := requestQueueControl(dir, rec.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if err := requestQueueControl(dir, rec.SendID, "release"); err == nil {
		t.Fatal("a second control request for the same entry was accepted")
	}
	if !processQueueControl("", dir, rec) {
		t.Fatal("worker did not handle cancellation")
	}
	result := queueControlResultForTest(t, dir, rec.SendID)
	stored, err := sendqueue.Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "cancelled" || result.Record == nil || result.Record.State != sendqueue.StateCancelled {
		t.Fatalf("cancel result: %+v", result)
	}
	if stored.State != sendqueue.StateCancelled || stored.Attempts != 0 || stored.SentAt != "" || !stored.Final() {
		t.Fatalf("cancelled entry could have been typed: %+v", stored)
	}
	if stored.SendID != rec.SendID || stored.Message != rec.Message || stored.CreatedAt != rec.CreatedAt {
		t.Fatalf("cancel did not preserve entry identity across save/load: %+v", stored)
	}
	if processQueueControl("", dir, rec) {
		t.Fatal("an answered request was handled twice")
	}
	for _, pending := range sendqueue.PendingTargets(dir) {
		if pending == rec.SessionID {
			t.Fatal("a cancelled entry still restarts its worker")
		}
	}
}

func TestQueueControlCannotCancelAfterTyping(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "0")
	dir, rec := queueControlFixture(t, sendqueue.StateSubmitted)
	rec.Attempts = 1
	rec.SentAt = time.Now().UTC().Format(time.RFC3339Nano)
	rec.DeliveryEvidence = map[string]interface{}{
		"delivery": "delivered", "submitted": true, "confirmation": "confirmed",
	}
	if err := sendqueue.Save(dir, rec); err != nil {
		t.Fatal(err)
	}
	if err := requestQueueControl(dir, rec.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	if !processQueueControl("", dir, rec) {
		t.Fatal("worker did not handle already sent entry")
	}
	result := queueControlResultForTest(t, dir, rec.SendID)
	stored, err := sendqueue.Load(dir, rec.SendID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != "already_sent" || result.Record == nil || result.Record.DeliveryEvidence["confirmation"] != "confirmed" {
		t.Fatalf("already sent result lost delivery evidence: %+v", result)
	}
	if stored.State != sendqueue.StateSubmitted || stored.Attempts != 1 || stored.DeliveryEvidence["submitted"] != true {
		t.Fatalf("cancel rewrote an already submitted entry: %+v", stored)
	}
	// Typed without child evidence (a worker restarted mid-send) is never
	// reported as already_sent.
	rec.State, rec.DeliveryEvidence = sendqueue.StateTyped, nil
	if got := queueAlreadySent(rec); got.Outcome != "unknown" {
		t.Fatalf("evidence-free typed entry: %+v", got)
	}
}

func TestQueueReleaseUsesReleaseChildAndKeepsEvidence(t *testing.T) {
	t.Setenv("AGENTDECK_EVENTS_BUS", "0")
	for _, tc := range []struct {
		name, childResult, wantState, wantReason string
		code                                     int
	}{
		{name: "submitted", childResult: `{"success":true,"delivery":"delivered","submitted":true,"confirmation":"confirmed"}`, wantState: sendqueue.StateSubmitted},
		{name: "composer blocked", childResult: `{"success":false,"delivery":"composer_blocked","error":"composer not safe; no keys typed"}`, wantState: sendqueue.StateQueued, wantReason: "composer_blocked", code: 1},
		{name: "menu open", childResult: `{"success":false,"delivery":"menu_open","error":"menu open"}`, wantState: sendqueue.StateTyped, wantReason: "menu_open", code: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, rec := queueControlFixture(t, sendqueue.StateQueued)
			// Evidence of an earlier attempt must not survive into this one.
			rec.DeliveryEvidence = map[string]interface{}{"delivery": "stale"}
			if err := sendqueue.Save(dir, rec); err != nil {
				t.Fatal(err)
			}
			previousRelease, previousNormal := sendChildRelease, sendChild
			t.Cleanup(func() { sendChildRelease, sendChild = previousRelease, previousNormal })
			sendChild = func(string, string, string, string) (int, func() int, error) {
				t.Fatal("release used the normal queued send child")
				return 0, nil, nil
			}
			sendChildRelease = func(profile, id, message, resultPath string) (int, func() int, error) {
				stored, err := sendqueue.Load(dir, rec.SendID)
				if err != nil || stored.State != sendqueue.StateTyping || stored.Attempts != 1 || stored.SentAt == "" ||
					stored.TargetStatus != queueReleaseStatus || stored.DeliveryEvidence != nil {
					t.Fatalf("release did not persist typing before the child: %+v %v", stored, err)
				}
				if id != rec.SessionID || message != rec.Message {
					t.Fatalf("child target %q or message %q changed", id, message)
				}
				if err := os.WriteFile(resultPath, []byte(tc.childResult), 0o600); err != nil {
					t.Fatal(err)
				}
				return 12345, func() int { return tc.code }, nil
			}
			set := func(fn func(*sendqueue.Record)) error {
				stored, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
				if err == nil {
					*rec = *stored
				}
				return err
			}
			if !typeQueued("", dir, rec, queueReleaseStatus, "", 0, set) {
				t.Fatal("release did not start")
			}
			stored, err := sendqueue.Load(dir, rec.SendID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.State != tc.wantState || stored.Attempts != 1 || stored.DeliveryEvidence["delivery"] == "stale" ||
				stored.DeliveryEvidence["delivery"] == nil || !strings.Contains(stored.Reason, tc.wantReason) {
				t.Fatalf("release outcome or evidence: %+v", stored)
			}
		})
	}
}

func TestQueueControlFilesArePrunedWithTheirRecord(t *testing.T) {
	dir, rec := queueControlFixture(t, sendqueue.StateCancelled)
	if err := requestQueueControl(dir, rec.SendID, "cancel"); err != nil {
		t.Fatal(err)
	}
	queueControlReply(dir, rec.SendID, queueControlResult{Outcome: "cancelled"})
	sendqueue.Prune(dir, time.Now().Add(time.Hour))
	for _, p := range []string{sendqueue.ControlPath(dir, rec.SendID), sendqueue.ControlResultPath(dir, rec.SendID)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s survived the prune: %v", filepath.Base(p), err)
		}
	}
}

func TestSleepUnlessQueueControlWakesForARequest(t *testing.T) {
	dir, rec := queueControlFixture(t, sendqueue.StateQueued)
	go func() {
		time.Sleep(100 * time.Millisecond)
		_ = requestQueueControl(dir, rec.SendID, "release")
	}()
	start := time.Now()
	sleepUnlessQueueControl(dir, rec.SessionID, 20*time.Second)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("backoff ignored the control request for %v", waited)
	}
}

func TestQueuedOutcomeCancelledIsNotADelivery(t *testing.T) {
	rec := &sendqueue.Record{State: sendqueue.StateCancelled}
	if got := queuedOutcome(rec); got != queuedOutcomeCancelled {
		t.Fatalf("cancelled send journaled as %q", got)
	}
}
