package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDiffChildEvents(t *testing.T) {
	running := func(id, title string) childRow {
		return childRow{ID: id, Title: title, Status: "running"}
	}

	t.Run("no changes emits nothing", func(t *testing.T) {
		prev := []childRow{running("a", "child-a")}
		curr := []childRow{running("a", "child-a")}
		if events := diffChildEvents(prev, curr); len(events) != 0 {
			t.Errorf("expected no events, got %+v", events)
		}
	})

	t.Run("new child emits added", func(t *testing.T) {
		prev := []childRow{running("a", "child-a")}
		curr := []childRow{running("a", "child-a"), running("b", "child-b")}
		events := diffChildEvents(prev, curr)
		want := []followEvent{{Event: "added", ID: "b", Title: "child-b", Status: "running"}}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("got %+v, want %+v", events, want)
		}
	})

	t.Run("status change emits transition", func(t *testing.T) {
		prev := []childRow{running("a", "child-a")}
		curr := []childRow{{ID: "a", Title: "child-a", Status: "waiting"}}
		events := diffChildEvents(prev, curr)
		want := []followEvent{{Event: "status", ID: "a", Title: "child-a", From: "running", To: "waiting"}}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("got %+v, want %+v", events, want)
		}
	})

	t.Run("new ledger entry emits done", func(t *testing.T) {
		prev := []childRow{{ID: "a", Title: "child-a", Status: "running"}}
		curr := []childRow{{ID: "a", Title: "child-a", Status: "idle",
			DoneStatus: "ok", DoneSummary: "all lint fixed", DoneAt: "2026-07-15T10:00:00Z"}}
		events := diffChildEvents(prev, curr)
		want := []followEvent{
			{Event: "status", ID: "a", Title: "child-a", From: "running", To: "idle"},
			{Event: "done", ID: "a", Title: "child-a",
				DoneStatus: "ok", DoneSummary: "all lint fixed", DoneAt: "2026-07-15T10:00:00Z"},
		}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("got %+v, want %+v", events, want)
		}
	})

	t.Run("re-asserted done emits done again", func(t *testing.T) {
		prev := []childRow{{ID: "a", Title: "child-a", Status: "idle",
			DoneStatus: "ok", DoneSummary: "round 1", DoneAt: "2026-07-15T10:00:00Z"}}
		curr := []childRow{{ID: "a", Title: "child-a", Status: "idle",
			DoneStatus: "fail", DoneSummary: "round 2 broke", DoneAt: "2026-07-15T11:00:00Z"}}
		events := diffChildEvents(prev, curr)
		want := []followEvent{{Event: "done", ID: "a", Title: "child-a",
			DoneStatus: "fail", DoneSummary: "round 2 broke", DoneAt: "2026-07-15T11:00:00Z"}}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("got %+v, want %+v", events, want)
		}
	})

	t.Run("removed child emits removed", func(t *testing.T) {
		prev := []childRow{running("a", "child-a"), running("b", "child-b")}
		curr := []childRow{running("a", "child-a")}
		events := diffChildEvents(prev, curr)
		want := []followEvent{{Event: "removed", ID: "b", Title: "child-b"}}
		if !reflect.DeepEqual(events, want) {
			t.Errorf("got %+v, want %+v", events, want)
		}
	})
}

func TestChildTerminal(t *testing.T) {
	cases := []struct {
		name string
		row  childRow
		want bool
	}{
		{"running is not terminal", childRow{Status: "running"}, false},
		{"waiting is not terminal", childRow{Status: "waiting"}, false},
		{"idle without sentinel is not terminal", childRow{Status: "idle"}, false},
		{"done sentinel is terminal", childRow{Status: "idle", DoneStatus: "ok"}, true},
		{"done fail sentinel is terminal", childRow{Status: "idle", DoneStatus: "fail"}, true},
		{"error is terminal", childRow{Status: "error"}, true},
		{"stopped is terminal", childRow{Status: "stopped"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := childTerminal(tc.row); got != tc.want {
				t.Errorf("childTerminal(%+v) = %v, want %v", tc.row, got, tc.want)
			}
		})
	}
}

func TestAllChildrenTerminal(t *testing.T) {
	if !allChildrenTerminal([]childRow{{Status: "error"}, {Status: "idle", DoneStatus: "ok"}}) {
		t.Error("expected all terminal")
	}
	if allChildrenTerminal([]childRow{{Status: "idle", DoneStatus: "ok"}, {Status: "running"}}) {
		t.Error("expected not all terminal while one is running")
	}
	if !allChildrenTerminal(nil) {
		t.Error("empty set is vacuously terminal")
	}
}

// The JSONL contract: every event marshals to a single flat object with
// stable keys — consumers (Monitor filters, jq) key off .event.
func TestFollowEventJSONShape(t *testing.T) {
	b, err := json.Marshal(followEvent{Event: "status", ID: "a", Title: "t", From: "running", To: "waiting"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event", "id", "title", "from", "to"} {
		if _, ok := m[key]; !ok {
			t.Errorf("missing key %q in %s", key, b)
		}
	}
	if _, ok := m["done_status"]; ok {
		t.Errorf("empty done_status should be omitted: %s", b)
	}
}
