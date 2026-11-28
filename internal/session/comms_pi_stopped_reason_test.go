package session

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// `pi-hooks status` names why socket wakes stopped for a pi parent. The reason
// must come from a failed wake that carried one of the stopped records: a
// failed wake from an earlier episode carried other records, and tries used up
// by wakes that went unanswered leave no failed record of their own.
func TestPiSocketWakesStopped_ReasonComesFromTheStoppedRecordsOwnWake(t *testing.T) {
	f := newPiParentFixture(t)
	dir := f.l.Reader().Dir
	older := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "first result"})
	f.commit(t, comms.Record{Kind: comms.KindWake, Via: "socket", State: comms.StateFailed,
		Err: "busy_for_5m", Refs: []string{older.ID}})
	fresh := f.commit(t, comms.Record{Kind: comms.KindTurn, Tier: comms.TierInfo, Text: "second result"})

	// Enrol the parent, and mark only the fresh record's tries as used up.
	if err := writeCommsEnrollment(f.parent.ID, CommsEnrollment{Dir: dir, Tool: "pi", At: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(commsEnrollmentPath(f.parent.ID)) })
	store, err := comms.ReadStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := comms.EnsureConsumer(dir, f.parent.ID, store, 0); err != nil {
		t.Fatal(err)
	}
	cursorOf := func(id string) string {
		t.Helper()
		bus, err := comms.OpenReaderDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = bus.Close() }()
		page, _, err := comms.Export(bus, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range page {
			if e.Record.ID == id {
				return strconv.FormatUint(uint64(e.Cursor), 10)
			}
		}
		t.Fatalf("record %s not in the ledger", id)
		return ""
	}
	setTries := func(tries map[string]int) {
		t.Helper()
		raw, err := os.ReadFile(comms.ConsumerPath(dir, f.parent.ID))
		if err != nil {
			t.Fatal(err)
		}
		var st map[string]any
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatal(err)
		}
		st["wake_tries"] = tries
		out, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(comms.ConsumerPath(dir, f.parent.ID), out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	setTries(map[string]int{cursorOf(fresh.ID): commsMaxWakeTries})

	if got := PiSocketWakesStopped(); len(got) != 0 {
		t.Fatalf("listed %+v: the only failed wake carried an earlier record, not the stopped one", got)
	}

	// The stopped record's own failed wake is the one named.
	f.commit(t, comms.Record{Kind: comms.KindWake, Via: "socket", State: comms.StateFailed,
		Err: "duplicate_for_5m", Refs: []string{fresh.ID}})
	got := PiSocketWakesStopped()
	if len(got) != 1 || got[0].Parent != f.parent.ID || got[0].Reason != "duplicate_for_5m" {
		t.Fatalf("listed %+v, want %s with duplicate_for_5m", got, f.parent.ID)
	}

	// A later failed wake for an earlier episode does not replace it.
	setTries(map[string]int{cursorOf(older.ID): 1, cursorOf(fresh.ID): commsMaxWakeTries})
	f.commit(t, comms.Record{Kind: comms.KindWake, Via: "socket", State: comms.StateFailed,
		Err: "socket_down_for_5m", Refs: []string{older.ID}})
	if got := PiSocketWakesStopped(); len(got) != 1 || got[0].Reason != "duplicate_for_5m" {
		t.Fatalf("listed %+v, want duplicate_for_5m: the newer failed wake carried only a record that is not stopped", got)
	}
}
