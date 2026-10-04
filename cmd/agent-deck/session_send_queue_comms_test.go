package main

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSessionSendQueuedSettledKeepsRealState(t *testing.T) {
	for _, tc := range []struct {
		state, want string
		settled     bool
	}{
		{sendqueue.StateSubmitted, comms.StateLanded, true},
		{sendqueue.StateTyped, comms.StateTyped, true},
		{sendqueue.StateFailed, comms.StateFailed, false},
		{sendqueue.StateLanded, comms.StateLanded, false},
		{sendqueue.StateSubmitted, "", false},
	} {
		t.Run(tc.state+"/"+tc.want, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Cleanup(session.SetCommsLedgerForTest(true))
			ledgerQueuedSend(&sendqueue.Record{SendID: "request", Sender: "sender", SessionID: "target", State: tc.state, Settled: tc.settled, Reason: "no transcript confirmation"}, false)
			entries, err := session.ReadCommsSpool("target")
			if err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(entries) != 0 {
					t.Fatalf("nonfinal send spooled: %+v", entries)
				}
				return
			}
			reason := "no transcript confirmation"
			if tc.settled {
				reason = "settled: " + reason
			}
			if len(entries) != 1 {
				t.Fatalf("entries: %+v", entries)
			}
			e := entries[0]
			if e.State != tc.want || e.Prompt != reason || e.From != "sender" || e.Event != "async" || e.Edge != session.CommsEdgeDelivery {
				t.Fatalf("outcome: %+v", e)
			}
		})
	}
}

// The worker may run under a different session after a restart. Both normal
// completion and settling without a state/reason change use the saved sender.
func TestSessionSendQueuedFinalReusesPersistentSender(t *testing.T) {
	for _, sender := range []string{"sender", sendSenderCLI} {
		t.Run(sender, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			t.Setenv("AGENTDECK_INSTANCE_ID", "unrelated-worker")
			t.Cleanup(session.SetCommsLedgerForTest(true))
			dir := t.TempDir()
			id, err := sendqueue.NextID(dir, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			prev := &sendqueue.Record{SendID: id, Sender: sender, SessionID: "target", State: sendqueue.StateSubmitted, Reason: "no transcript confirmation"}
			if err := sendqueue.Save(dir, prev); err != nil {
				t.Fatal(err)
			}
			final, err := sendqueue.Load(dir, prev.SendID)
			if err != nil {
				t.Fatal(err)
			}
			final.Settled = true
			queuedSendChanged("default", prev, final)
			queuedSendChanged("default", final, final)
			entries, err := session.ReadCommsSpool("target")
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0].From != sender || entries[0].State != comms.StateLanded || entries[0].Ref != prev.SendID {
				t.Fatalf("final receipt must use the saved sender exactly once: %+v", entries)
			}
		})
	}
}
