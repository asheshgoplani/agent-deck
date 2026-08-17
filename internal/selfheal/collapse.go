package selfheal

import (
	"strings"
	"sync"
	"time"
)

// DefaultAuditHeartbeat is how often an UNCHANGED session is re-recorded.
//
// It is the liveness floor of the audit: without it, a fleet that is entirely
// healthy for six hours writes nothing, and "self-heal recorded nothing" would
// be indistinguishable from "self-heal was not running". At 15 minutes a
// 34-session fleet writes ~3.3k heartbeat records/day (~1 MB/day) — three
// orders of magnitude under the 480 MB/day the uncollapsed stream produced,
// and still fine-grained enough to watch a wedged session's dwell and caps
// evolve while it sits in one state.
const DefaultAuditHeartbeat = 15 * time.Minute

// CollapsingSink writes one record per session STATE, not one per evaluation.
//
// WHY: the pass evaluates every session on every poll and recorded each read.
// Measured, that was 7.4M records / 2.55 GB in 5 days, of which nearly all were
// a healthy session re-recorded every 2 seconds — a record that carries no
// information the previous 40,000 identical records did not.
//
// WHAT IS PRESERVED. The stream stays fully reconstructable:
//   - the FIRST read of a state is written immediately (an operator tailing the
//     audit sees a session change state at once, not one heartbeat later);
//   - identical follow-up reads are suppressed, but COUNTED;
//   - when the state changes, a CLOSING copy of the run's record is written
//     first, carrying the last suppressed read's timestamp and the suppressed
//     count, so the run's exact extent is on record before the new state's
//     entry;
//   - an unchanged state is re-recorded every heartbeat, again with the count.
//
// So for any session, at any moment in the window, the audit still answers
// "what state was it in, since when, and how many reads confirmed it". What it
// no longer answers is "what was the output signature on read #17,432 of an
// unchanging run" — which is the data that cost 480 MB/day.
//
// WHAT IS NEVER COLLAPSED: any record where an action actually ran. Those are
// the decisions the audit exists to prove, and they are never merged with a
// neighbour even if two consecutive ones look identical.
//
// Append is safe for concurrent callers.
//
// MEMORY: one open run (~one Event) per session id ever seen, for the life of
// the process. That matches the lifetime of the engine's existing per-session
// maps (prevSig, substateSeen) rather than introducing a new class of growth,
// and Flush is what drains it at shutdown.
type CollapsingSink struct {
	next      EventSink
	heartbeat time.Duration

	mu  sync.Mutex
	run map[string]*collapseRun
}

// collapseRun is the open run of identical evaluations for one session.
type collapseRun struct {
	sig        string
	emitted    Event     // the record written when the run opened (or last beat)
	emittedAt  time.Time // when that record was written
	suppressed int       // identical reads not written since then
	lastTS     string    // timestamp of the most recent suppressed read
}

// NewCollapsingSink wraps next so consecutive identical evaluations of the same
// session collapse into one record plus a repeat count. heartbeat <= 0 uses
// DefaultAuditHeartbeat.
func NewCollapsingSink(next EventSink, heartbeat time.Duration) *CollapsingSink {
	if heartbeat <= 0 {
		heartbeat = DefaultAuditHeartbeat
	}
	return &CollapsingSink{next: next, heartbeat: heartbeat, run: map[string]*collapseRun{}}
}

// collapseSig is the part of an event that carries state. Deliberately EXCLUDED:
// the timestamp and the read signatures (they move on every poll — that is the
// whole 480 MB/day), the dwell (it grows monotonically inside one state and is
// derivable from the state's entry timestamp), and the caps counters (the global
// counter moves when OTHER sessions act, which would de-collapse every session
// in the fleet at once; caps are recorded on the act and gate records where they
// decide something).
func collapseSig(e Event) string {
	var b strings.Builder
	b.WriteString(string(e.Decision))
	b.WriteByte('|')
	b.WriteString(e.Substate)
	b.WriteByte('|')
	b.WriteString(string(e.Action))
	b.WriteByte('|')
	b.WriteString(string(e.WouldHave))
	b.WriteByte('|')
	b.WriteString(e.Outcome)
	b.WriteByte('|')
	b.WriteString(string(e.Stage))
	return b.String()
}

// Append records the event, collapsing it into the session's open run when it
// says nothing new.
func (c *CollapsingSink) Append(e Event) error {
	// A record where an action ran is always written, and it also ends any open
	// run: the run's extent must be on record before the action is.
	if e.Action != ActionNone {
		c.mu.Lock()
		flush := c.takeClosingLocked(e.SessionID)
		c.mu.Unlock()
		if flush != nil {
			_ = c.next.Append(*flush)
		}
		return c.next.Append(e)
	}

	at, err := time.Parse(time.RFC3339, e.TS)
	if err != nil {
		// An unreadable clock is not a reason to drop an audit record. Write it
		// and reset the run so the next read starts from a known state.
		c.mu.Lock()
		delete(c.run, e.SessionID)
		c.mu.Unlock()
		return c.next.Append(e)
	}

	sig := collapseSig(e)

	c.mu.Lock()
	run, open := c.run[e.SessionID]
	switch {
	case !open || run.sig != sig:
		// State entry. Close the previous run first (if it suppressed anything),
		// then open this one.
		var closing *Event
		if open && run.suppressed > 0 {
			ev := run.emitted
			ev.Repeat = run.suppressed
			ev.TS = run.lastTS
			closing = &ev
		}
		c.run[e.SessionID] = &collapseRun{sig: sig, emitted: e, emittedAt: at}
		c.mu.Unlock()
		if closing != nil {
			_ = c.next.Append(*closing)
		}
		return c.next.Append(e)

	case at.Sub(run.emittedAt) >= c.heartbeat:
		// Heartbeat: same state, but long enough that the audit should say so.
		// The record describes THIS read and carries the reads it stands in for.
		beat := e
		beat.Repeat = run.suppressed
		run.emitted = e
		run.emittedAt = at
		run.suppressed = 0
		run.lastTS = ""
		c.mu.Unlock()
		return c.next.Append(beat)

	default:
		run.suppressed++
		run.lastTS = e.TS
		c.mu.Unlock()
		return nil
	}
}

// takeClosingLocked removes a session's open run and returns the closing record
// it owes, if any. Callers must hold c.mu.
func (c *CollapsingSink) takeClosingLocked(sessionID string) *Event {
	run, open := c.run[sessionID]
	if !open {
		return nil
	}
	delete(c.run, sessionID)
	if run.suppressed == 0 {
		return nil
	}
	ev := run.emitted
	ev.Repeat = run.suppressed
	ev.TS = run.lastTS
	return &ev
}

// Flush writes the closing record for every open run, so a shutdown does not
// leave the last run of each session unaccounted for.
func (c *CollapsingSink) Flush() error {
	c.mu.Lock()
	ids := make([]string, 0, len(c.run))
	for id := range c.run {
		ids = append(ids, id)
	}
	closings := make([]Event, 0, len(ids))
	for _, id := range ids {
		if ev := c.takeClosingLocked(id); ev != nil {
			closings = append(closings, *ev)
		}
	}
	c.mu.Unlock()
	var firstErr error
	for _, ev := range closings {
		if err := c.next.Append(ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
