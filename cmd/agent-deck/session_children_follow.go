package main

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// childRow is one child in `session children` output — shared by the one-shot
// listing and the --follow stream.
type childRow struct {
	ID                     string `json:"id"`
	Title                  string `json:"title"`
	Status                 string `json:"status"`
	PeerName               string `json:"peer_name,omitempty"`
	PeerMessagingCandidate bool   `json:"peer_messaging_candidate,omitempty"`
	DoneStatus             string `json:"done_status,omitempty"`
	DoneSummary            string `json:"done_summary,omitempty"`
	DoneAt                 string `json:"done_at,omitempty"`
	// DoneStale marks a completion that predates the last message we delivered
	// to this child: the child asserted "done" for EARLIER work, then was given
	// something new. Without it a parent polling the ledger reads the previous
	// turn's completion as an answer to the current one — a session appearing
	// to replay an old completion message instead of doing the new work.
	DoneStale bool `json:"done_stale,omitempty"`
	// LastSentAt is when a message was last delivered to this child (RFC3339,
	// from the last_sent_at clock `session send` stamps). Present so a parent
	// can audit the staleness verdict rather than trust it blind.
	LastSentAt string `json:"last_sent_at,omitempty"`
	// ContextTokens is the child's current context size — the prompt tokens of
	// the newest assistant turn in its Claude transcript. A supervising parent
	// uses it to catch a child approaching the context limit and rotate it to
	// a fresh session instead of letting it degrade into auto-compaction.
	// Absent for non-Claude tools or before the first assistant turn.
	ContextTokens int `json:"context_tokens,omitempty"`
}

// followEvent is one JSONL line on the --follow stream. Consumers key off
// .event: snapshot|added|status|done|removed|error.
type followEvent struct {
	Event                  string `json:"event"`
	ID                     string `json:"id,omitempty"`
	Title                  string `json:"title,omitempty"`
	Status                 string `json:"status,omitempty"`
	PeerName               string `json:"peer_name,omitempty"`
	PeerMessagingCandidate bool   `json:"peer_messaging_candidate,omitempty"`
	From                   string `json:"from,omitempty"`
	To                     string `json:"to,omitempty"`
	DoneStatus             string `json:"done_status,omitempty"`
	DoneSummary            string `json:"done_summary,omitempty"`
	DoneAt                 string `json:"done_at,omitempty"`
	DoneStale              bool   `json:"done_stale,omitempty"`
	LastSentAt             string `json:"last_sent_at,omitempty"`
	ContextTokens          int    `json:"context_tokens,omitempty"`
	Error                  string `json:"error,omitempty"`
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
	// DoneStale counts children whose only completion predates the work they
	// were last given. They are NOT counted in done_ok/done_fail: that
	// completion answers an earlier turn.
	DoneStale int `json:"done_stale"`
}

// completionStaleGrace absorbs the second-granularity of the last_sent_at clock
// (stored as Unix seconds, so it can read up to a second earlier than the real
// send) and any sub-second skew between the two writers. A completion asserted
// within this window of a delivery is still treated as belonging to the work
// that came BEFORE it — no agent finishes real work in under a second, and
// under-reporting staleness is what lets an old report pass as a new one.
const completionStaleGrace = time.Second

// completionIsStale reports whether a completion asserted at finishedAt answers
// work that was superseded by a delivery at lastSentAt. Unknown clocks (zero on
// either side) are never stale — we only downgrade a report we can prove old.
func completionIsStale(finishedAt, lastSentAt time.Time) bool {
	if finishedAt.IsZero() || lastSentAt.IsZero() {
		return false
	}
	return finishedAt.Before(lastSentAt.Add(completionStaleGrace))
}

// responseIsStale reports whether a transcript response carrying the given
// RFC3339 timestamp predates the last message delivered to that session.
// An unparseable or absent timestamp is never stale — same rule as
// completionIsStale: only a provably old report is downgraded.
func responseIsStale(timestamp string, lastSentAt time.Time) bool {
	ts, ok := parseResponseTimestamp(timestamp)
	if !ok {
		return false
	}
	return completionIsStale(ts, lastSentAt)
}

// parseResponseTimestamp accepts the RFC3339 shapes Claude writes into its
// JSONL (nanosecond and second precision).
func parseResponseTimestamp(timestamp string) (time.Time, bool) {
	if timestamp == "" {
		return time.Time{}, false
	}
	if ts, err := time.Parse(time.RFC3339Nano, timestamp); err == nil {
		return ts, true
	}
	if ts, err := time.Parse(time.RFC3339, timestamp); err == nil {
		return ts, true
	}
	return time.Time{}, false
}

// sendStateDB resolves the state DB a CLI command should write session clocks
// to: the handle it already opened, falling back to the process global (set
// only on the TUI/daemon startup path) when storage is unavailable.
func sendStateDB(storage *session.Storage) *statedb.StateDB {
	if storage != nil {
		if db := storage.GetDB(); db != nil {
			return db
		}
	}
	return statedb.GetGlobal()
}

// lastSentClock reads a session's last_sent_at clock. Zero time when the
// session was never sent to, or when no state DB is available.
func lastSentClock(db *statedb.StateDB, id string) time.Time {
	if db == nil {
		return time.Time{}
	}
	ts, err := db.ReadLastSentAt(id)
	if err != nil || ts <= 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0)
}

// childCompletion resolves a child's last asserted completion from the two
// sources that can know one, in order:
//
//  1. The completion ledger — durable and last-wins, but written ONLY by the
//     transition daemon.
//  2. The child's own Claude transcript — the sentinel the worker actually
//     printed, read directly.
//
// Source 2 exists because source 1 has a single point of failure with no
// visible symptom. The daemon is a launchd/systemd unit; when it is down the
// ledger simply stops being written and every child reads done_status=null
// forever — identical to "nobody has finished". Observed 2026-07-20: a macOS
// Background Task Management LWCR mismatch (the binary was reinstalled, which
// invalidates the registered code requirement) left the notifier crash-looping
// on EX_CONFIG for a week. Seven workers printed the sentinel; the ledger
// recorded none of them; every `until … done_status != null` loop the fleet and
// orchestrate skills prescribe hung until its harness killed it, and the
// turn-start fleet snapshot reported "0 done" for a fleet that was finished.
//
// The ledger still wins when present: it is durable across the child being
// handed new work, whereas the transcript only ever answers "did the CURRENT
// turn finish" (by design — see TranscriptDoneSignal). So the fallback strictly
// adds signal where there was none and cannot resurrect a superseded report.
func childCompletion(k *session.Instance) (status, summary string, finishedAt time.Time, ok bool) {
	if e, found := session.ReadLedgerEntry(k.ID); found {
		return e.Status, e.Summary, e.FinishedAt, true
	}
	sig, at, found := session.TranscriptDoneSignal(session.ClaudeTranscriptPathForInstance(k))
	if !found {
		return "", "", time.Time{}, false
	}
	return sig.Status, sig.Summary, at, true
}

// buildChildRows converts refreshed child instances into rows. Callers must
// have run session.RefreshInstancesForCLIStatus on kids first so UpdateStatus
// sees warm tmux caches and hook statuses (issue #610).
//
// db supplies the last_sent_at clock used to age out completion reports; pass
// nil to skip staleness classification (every completion then reads as fresh,
// the pre-existing behavior).
func buildChildRows(kids []*session.Instance, db *statedb.StateDB) []childRow {
	rows := make([]childRow, 0, len(kids))
	for _, k := range kids {
		_ = k.UpdateStatus()
		row := childRow{ID: k.ID, Title: k.Title, Status: StatusString(k.Status)}
		if k.PeerMessagingCandidate() {
			row.PeerName = k.ClaudePeerName()
			row.PeerMessagingCandidate = true
		}
		lastSent := lastSentClock(db, k.ID)
		if !lastSent.IsZero() {
			row.LastSentAt = lastSent.Format(time.RFC3339)
		}
		if status, summary, finishedAt, ok := childCompletion(k); ok {
			row.DoneStatus = status
			row.DoneSummary = summary
			if !finishedAt.IsZero() {
				row.DoneAt = finishedAt.Format(time.RFC3339)
			}
			row.DoneStale = completionIsStale(finishedAt, lastSent)
		}
		if tokens, ok := session.CurrentContextTokensForInstance(k); ok {
			row.ContextTokens = tokens
		}
		rows = append(rows, row)
	}
	return rows
}

// parentContextFields renders a parent's own context size for `session
// children` output: the suffix appended to the human header, and whether the
// JSON payload should carry parent_context_tokens.
//
// Unresolvable (non-Claude parent, no assistant turn yet) and zero both mean
// "unknown", and both must stay OUT of the JSON rather than surface as 0 — a
// supervisor thresholding on the field would read a zero as "context is
// empty" and skip the very rotation the field exists to trigger.
func parentContextFields(tokens int, ok bool) (suffix string, emit bool) {
	if !ok || tokens <= 0 {
		return "", false
	}
	return fmt.Sprintf("  self-ctx=%dk", tokens/1000), true
}

func snapshotEvent(event string, r childRow) followEvent {
	return followEvent{
		Event: event, ID: r.ID, Title: r.Title, Status: r.Status,
		PeerName: r.PeerName, PeerMessagingCandidate: r.PeerMessagingCandidate,
		DoneStatus: r.DoneStatus, DoneSummary: r.DoneSummary, DoneAt: r.DoneAt,
		DoneStale: r.DoneStale, LastSentAt: r.LastSentAt, ContextTokens: r.ContextTokens,
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
		// A done event announces a NEW completion. A ledger entry that is stale
		// (predates the work this child was last given) is not one: emitting it
		// would hand the consumer the previous turn's report as if it answered
		// the current turn. It still rides along on snapshot/added rows with
		// done_stale set, so nothing is hidden.
		if r.DoneStatus != "" && !r.DoneStale &&
			(old.DoneStatus != r.DoneStatus || old.DoneSummary != r.DoneSummary || old.DoneAt != r.DoneAt || old.DoneStale) {
			events = append(events, followEvent{
				Event: "done", ID: r.ID, Title: r.Title,
				DoneStatus: r.DoneStatus, DoneSummary: r.DoneSummary, DoneAt: r.DoneAt,
				LastSentAt: r.LastSentAt,
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
//
// A STALE sentinel is not terminal either: the child asserted done for earlier
// work and has since been given something new. Treating it as terminal is what
// let `--until-done` return immediately after a follow-up send, handing the
// parent the previous turn's completion as the answer to the new work.
func childTerminal(r childRow) bool {
	if r.DoneStatus != "" && !r.DoneStale {
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
		if r.DoneStatus != "" && r.DoneStale {
			s.DoneStale++
			continue
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

// pollFollowOwnerRunning reports whether the agent session that started a
// follow stream is still executing a turn. Codex keeps subprocess pipes open
// across completed turns, so stdout never closes and ordinary SIGPIPE cleanup
// cannot reap a forgotten follower. Keep this injectable for loop tests.
var pollFollowOwnerRunning = loadFollowOwnerRunning

func loadFollowOwnerRunning(profile, ownerID string) (bool, error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return false, err
	}
	defer func() { _ = storage.Close() }()

	db := storage.GetDB()
	if db == nil {
		return false, nil
	}
	owner, err := db.LoadInstanceByID(ownerID)
	if err != nil {
		return false, err
	}
	return owner != nil && owner.Status == string(session.StatusRunning), nil
}

// loadChildRows does one full poll: fresh DB read, parent existence check,
// status refresh, row build. Storage is closed before returning so the poll
// loop never accumulates DB handles.
func loadChildRows(profile, parentID string) ([]childRow, error) {
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = storage.Close() }()

	kids, parentExists, err := storage.LoadChildInstances(parentID)
	if err != nil {
		return nil, err
	}
	if !parentExists {
		return nil, fmt.Errorf("parent session %s no longer exists", parentID)
	}

	session.RefreshInstancesForCLIStatus(kids)
	return buildChildRows(kids, storage.GetDB()), nil
}

// runChildrenFollow streams child state changes as JSONL until interrupted,
// --until-done is satisfied, its agent owner finishes a turn, or polling fails
// repeatedly. Every terminal state is visible on the stream (done ok/fail,
// error, stopped, removed) so silence only ever means "nothing changed" — the
// heartbeat line proves liveness.
//
// interval must be positive; the caller validates it, since a non-positive
// sleep would turn the poll into a busy-loop that reopens storage every pass.
//
// With untilDone, a parent that currently has no children is already "all
// terminal" and completes immediately. Callers that mean to watch for children
// yet to be spawned must attach after at least one child exists.
func runChildrenFollow(profile, parentID, ownerID string, interval, heartbeat time.Duration, untilDone bool, w io.Writer) int {
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
	ownerNotRunning := 0
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

		// Agent Deck sessions move away from running when their turn finishes.
		// Two consecutive observations avoid a launch-time status race. Shell
		// callers pass no ownerID and retain the traditional unbounded stream.
		if ownerID != "" {
			if running, err := pollFollowOwnerRunning(profile, ownerID); err == nil {
				if running {
					ownerNotRunning = 0
				} else {
					ownerNotRunning++
					if ownerNotRunning >= 2 {
						return 0
					}
				}
			}
		}
		time.Sleep(interval)
	}
}
