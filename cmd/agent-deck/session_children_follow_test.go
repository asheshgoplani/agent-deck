package main

import (
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"testing"
	"time"
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

// summarizeChildren drives both the heartbeat and complete lines, so every
// counter it reports is part of the JSONL contract.
func TestSummarizeChildren(t *testing.T) {
	rows := []childRow{
		{Status: "running"},
		{Status: "waiting"},
		{Status: "waiting"},
		{Status: "idle", DoneStatus: "ok"},
		{Status: "error", DoneStatus: "fail"},
		{Status: "idle"}, // Neither running/waiting nor done: counted only in Children.
	}
	got := summarizeChildren("heartbeat", rows)
	want := followSummary{Event: "heartbeat", Children: 6, Running: 1, Waiting: 2, DoneOK: 1, DoneFail: 1}
	if got != want {
		t.Errorf("summarizeChildren() = %+v, want %+v", got, want)
	}

	empty := summarizeChildren("complete", nil)
	if empty != (followSummary{Event: "complete"}) {
		t.Errorf("empty summary = %+v, want zeroed counts with event=complete", empty)
	}
}

// errWriter fails every write, standing in for a consumer that went away
// mid-stream (`agent-deck session children --follow | head -1`).
type errWriter struct{ writes int }

func (e *errWriter) Write(p []byte) (int, error) {
	e.writes++
	return 0, io.ErrClosedPipe
}

// A dead stream must end the poll loop. Before emit reported write failures,
// the loop kept polling forever with nowhere to write the results.
func TestRunChildrenFollowStopsOnDeadStream(t *testing.T) {
	for _, tc := range []struct {
		name string
		poll func(profile, parentID string) ([]childRow, error)
	}{
		{
			name: "snapshot write fails",
			poll: func(string, string) ([]childRow, error) {
				return []childRow{{ID: "a", Status: "running"}}, nil
			},
		},
		{
			name: "error-event write fails",
			poll: func(string, string) ([]childRow, error) {
				return nil, errors.New("poll failed")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			polls := 0
			restore := pollChildRows
			pollChildRows = func(profile, parentID string) ([]childRow, error) {
				polls++
				return tc.poll(profile, parentID)
			}
			t.Cleanup(func() { pollChildRows = restore })

			w := &errWriter{}
			done := make(chan int, 1)
			go func() {
				done <- runChildrenFollow("p", "parent", time.Millisecond, 0, false, w)
			}()

			select {
			case code := <-done:
				if code != 0 {
					t.Errorf("exit code = %d, want 0 (stream closed, not a poll failure)", code)
				}
				if w.writes != 1 {
					t.Errorf("writes = %d, want 1 — loop kept emitting after the stream died", w.writes)
				}
				if polls != 1 {
					t.Errorf("polls = %d, want 1 — loop kept polling after the stream died", polls)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("runChildrenFollow kept polling after the stream closed")
			}
		})
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
