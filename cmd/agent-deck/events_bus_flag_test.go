package main

import (
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/comms"
)

// `events follow|stats --bus comms` opens the comms ledger read-only and
// says so when there is none yet.
func TestOpenBusForRead(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENTDECK_PROFILE", "default")
	if _, err := openBusForRead("nope"); err == nil {
		t.Fatal("unknown bus accepted")
	}
	if _, err := openBusForRead("comms"); !errors.Is(err, comms.ErrNoLedger) {
		t.Fatalf("no ledger yet: %v", err)
	}
	l, err := comms.Open("default")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Commit(comms.Record{Kind: comms.KindTurn, From: "c", Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
	bus, err := openBusForRead("comms")
	if err != nil {
		t.Fatal(err)
	}
	defer bus.Close()
	if !bus.ReadOnly() {
		t.Fatal("comms bus must open read-only")
	}
	recs, _, err := comms.ReadAfter(bus, 0, 0)
	if err != nil || len(recs) != 1 || recs[0].Text != "hi" {
		t.Fatalf("ReadAfter via the CLI bus: %+v err %v", recs, err)
	}
	if kinds, err := bus.KindCounts(); err != nil || kinds[comms.KindTurn] != 1 {
		t.Fatalf("stats kinds: %v err %v", kinds, err)
	}
}
