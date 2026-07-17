package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// childRow is one child in `session children` output — shared by the one-shot
// listing and the --follow stream.
type childRow struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	DoneStatus  string `json:"done_status,omitempty"`
	DoneSummary string `json:"done_summary,omitempty"`
	DoneAt      string `json:"done_at,omitempty"`
}

// followEvent is one JSONL line on the --follow stream. Consumers key off
// .event: snapshot|added|status|done|removed|error.
type followEvent struct {
	Event       string `json:"event"`
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"`
	From        string `json:"from,omitempty"`
	To          string `json:"to,omitempty"`
	DoneStatus  string `json:"done_status,omitempty"`
	DoneSummary string `json:"done_summary,omitempty"`
	DoneAt      string `json:"done_at,omitempty"`
	Error       string `json:"error,omitempty"`
}

// followSummary is the heartbeat/complete line. Counts have no omitempty so a
// zero reads as a zero, not a missing key.
type followSummary struct {
	Event    string `json:"event"`
	Children int    `json:"children"`
	Running  int    `json:"running"`
	Waiting  int    `json:"waiting"`
	DoneOK   int    `json:"done_ok"`
	DoneFail int    `json:"done_fail"`
}

// buildChildRows converts refreshed child instances into rows. Callers must
// have run session.RefreshInstancesForCLIStatus on kids first so UpdateStatus
// sees warm tmux caches and hook statuses (issue #610).
func buildChildRows(kids []*session.Instance) []childRow {
	rows := make([]childRow, 0, len(kids))
	for _, k := range kids {
		_ = k.UpdateStatus()
		row := childRow{ID: k.ID, Title: k.Title, Status: StatusString(k.Status)}
		if e, ok := session.ReadLedgerEntry(k.ID); ok {
			row.DoneStatus = e.Status
			row.DoneSummary = e.Summary
			if !e.FinishedAt.IsZero() {
				row.DoneAt = e.FinishedAt.Format(time.RFC3339)
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func snapshotEvent(event string, r childRow) followEvent {
	return followEvent{
		Event: event, ID: r.ID, Title: r.Title, Status: r.Status,
		DoneStatus: r.DoneStatus, DoneSummary: r.DoneSummary, DoneAt: r.DoneAt,
	}
}

// diffChildEvents computes the events between two polls. Order: current
// children first (added, then status, then done per child), removals last.
func diffChildEvents(prev, curr []childRow) []followEvent {
	prevByID := make(map[string]childRow, len(prev))
	for _, r := range prev {
		prevByID[r.ID] = r
	}
	currIDs := make(map[string]bool, len(curr))
	var events []followEvent
	for _, r := range curr {
		currIDs[r.ID] = true
		old, seen := prevByID[r.ID]
		if !seen {
			events = append(events, snapshotEvent("added", r))
			continue
		}
		if old.Status != r.Status {
			events = append(events, followEvent{
				Event: "status", ID: r.ID, Title: r.Title, From: old.Status, To: r.Status,
			})
		}
		if r.DoneStatus != "" &&
			(old.DoneStatus != r.DoneStatus || old.DoneSummary != r.DoneSummary || old.DoneAt != r.DoneAt) {
			events = append(events, followEvent{
				Event: "done", ID: r.ID, Title: r.Title,
				DoneStatus: r.DoneStatus, DoneSummary: r.DoneSummary, DoneAt: r.DoneAt,
			})
		}
	}
	for _, r := range prev {
		if !currIDs[r.ID] {
			events = append(events, followEvent{Event: "removed", ID: r.ID, Title: r.Title})
		}
	}
	return events
}

// childTerminal reports whether a child needs no further supervision: it
// asserted the completion sentinel (ok or fail), or its process is gone.
// idle WITHOUT a sentinel is not terminal — an agent parked at the prompt may
// just be between turns, and treating it as done would end --until-done early.
func childTerminal(r childRow) bool {
	if r.DoneStatus != "" {
		return true
	}
	return r.Status == "error" || r.Status == "stopped"
}

func allChildrenTerminal(rows []childRow) bool {
	for _, r := range rows {
		if !childTerminal(r) {
			return false
		}
	}
	return true
}

func summarizeChildren(event string, rows []childRow) followSummary {
	s := followSummary{Event: event, Children: len(rows)}
	for _, r := range rows {
		switch r.Status {
		case "running":
			s.Running++
		case "waiting":
			s.Waiting++
		}
		switch r.DoneStatus {
		case "ok":
			s.DoneOK++
		case "fail":
			s.DoneFail++
		}
	}
	return s
}

// pollChildRows is the poll runChildrenFollow performs each tick. It is a var
// so tests can drive the loop without standing up storage.
var pollChildRows = loadChildRows

// loadChildRows does one full poll: fresh DB read, parent existence check,
// status refresh, row build. Storage is closed before returning so the poll
// loop never accumulates DB handles.
func loadChildRows(profile, parentID string) ([]childRow, error) {
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = storage.Close() }()

	parentExists := false
	for _, inst := range instances {
		if inst != nil && inst.ID == parentID {
			parentExists = true
			break
		}
	}
	if !parentExists {
		return nil, fmt.Errorf("parent session %s no longer exists", parentID)
	}

	kids := childrenOf(parentID, instances)
	session.RefreshInstancesForCLIStatus(kids)
	return buildChildRows(kids), nil
}

// runChildrenFollow streams child state changes as JSONL until interrupted,
// --until-done is satisfied, or polling fails repeatedly. Every terminal state
// is visible on the stream (done ok/fail, error, stopped, removed) so silence
// only ever means "nothing changed" — the heartbeat line proves liveness.
//
// interval must be positive; the caller validates it, since a non-positive
// sleep would turn the poll into a busy-loop that reopens storage every pass.
//
// With untilDone, a parent that currently has no children is already "all
// terminal" and completes immediately. Callers that mean to watch for children
// yet to be spawned must attach after at least one child exists.
func runChildrenFollow(profile, parentID string, interval, heartbeat time.Duration, untilDone bool, w io.Writer) int {
	// emit reports whether the stream is still writable. A consumer that goes
	// away mid-stream (`... | head -1`) makes every later write fail; without
	// this the loop would keep polling the DB forever with nowhere to write.
	emit := func(v interface{}) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true // Malformed event, not a dead stream: keep polling.
		}
		_, err = fmt.Fprintln(w, string(b))
		return err == nil
	}

	const maxConsecutiveFailures = 5
	var prev []childRow
	first := true
	failures := 0
	lastHeartbeat := time.Now()

	for {
		rows, err := pollChildRows(profile, parentID)
		if err != nil {
			failures++
			if !emit(followEvent{Event: "error", Error: err.Error()}) {
				return 0
			}
			if failures >= maxConsecutiveFailures {
				return 1
			}
		} else {
			failures = 0
			if first {
				for _, r := range rows {
					if !emit(snapshotEvent("snapshot", r)) {
						return 0
					}
				}
				first = false
			} else {
				for _, e := range diffChildEvents(prev, rows) {
					if !emit(e) {
						return 0
					}
				}
			}
			prev = rows
			if untilDone && allChildrenTerminal(rows) {
				_ = emit(summarizeChildren("complete", rows))
				return 0
			}
		}

		if heartbeat > 0 && time.Since(lastHeartbeat) >= heartbeat {
			if !emit(summarizeChildren("heartbeat", prev)) {
				return 0
			}
			lastHeartbeat = time.Now()
		}
		time.Sleep(interval)
	}
}
