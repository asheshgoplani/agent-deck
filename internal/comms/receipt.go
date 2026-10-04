package comms

import (
	"errors"
	"fmt"
	"sort"

	"github.com/asheshgoplani/agent-deck/internal/events"
)

// Consumer contract (docs/comms.md "Consumers, receipts and retention").
//
// Four rules folded in from the MonoCode relay comparison
// (docs/monocode-comms-20261003.md in the conductor's repo):
//
//  1. Every send carries a caller request id (Record.Req) and its receipt
//     is the `send` record itself, committed (durable) BEFORE the action is
//     taken; a retry with the same id gets ErrDuplicate with the stored
//     record and never causes a second delivery (Ledger.Commit, SendKey).
//     In the ledger now (G3/G6); `session send` writes it in P2.
//  2. Reads of a session are bounded: at most N recent records, a per
//     message byte cap (CapText), a cursor for older ones (ReadAfter with a
//     limit, Exported.Cursor), tool noise never stored (records carry the
//     final text only). ReadAfter is here; `msg read --last N` is P2.
//  3. Delivery to an idle parent is one combined wake carrying every pending
//     record (Digest); each record's attempt moves to `attempted` and, if
//     the parent's turn fails, back to pending by a `failed` receipt and a
//     Retry; automatic wakes per parent are capped (MaxAutoWakes) and a
//     parent past the cap waits for a human or its own next turn. P2.
//  4. Where agent-deck launches the harness itself, a producer may read the
//     harness's own protocol stream (Claude stream-json, Codex app-server,
//     ACP, pi rpc, OpenCode SSE) instead of hooks; it spools the same
//     CommsSpoolEntry edges, so the ledger does not care which. P3 or later,
//     per harness, with the same fixture rule as any producer.
// Frozen in P0 so the P2 consumers, the P3 transport and a later workflow
// runner build on one shape: a receipt per (message, recipient, consumer
// generation, attempt) with evidence states that only move forward, and a
// per-consumer acknowledgement state that is a contiguous watermark plus
// sparse acknowledgements, never a scalar cursor that can skip pending
// records when an urgent one overtakes them.

// Receipt evidence states, weakest first. A consumer may report any state
// it can observe; it never reports a stronger one than it has evidence for.
const (
	// ReceiptDurable: the record is committed to the recipient's ledger.
	ReceiptDurable = "durable"
	// ReceiptAttempted: a delivery was started (typed, injected, pushed).
	ReceiptAttempted = "attempted"
	// ReceiptTransportAccepted: the transport reported success (tmux saw
	// the line submitted, the socket acked, the hook printed its context).
	ReceiptTransportAccepted = "transport_accepted"
	// ReceiptContextObserved: a later prompt-start hook found the record id
	// in the model's prompt: the model was shown it.
	ReceiptContextObserved = "context_observed"
	// ReceiptApplicationAcked: the consumer explicitly acknowledged the
	// record (`msg ack`), or the application confirmed the effect.
	ReceiptApplicationAcked = "application_acked"
	// ReceiptFailed is terminal for one attempt; a new attempt starts over
	// from ReceiptDurable.
	ReceiptFailed = "failed"
	// ReceiptUnknown: the attempt was made but the adapter cannot observe
	// landing (a typed line into a harness with no prompt-start hook).
	// Reached from attempted (or a retry's attempted); it leaves only on
	// positive landing evidence (context_observed, application_acked),
	// never on a timeout and never to transport_accepted.
	ReceiptUnknown = "unknown"
)

var receiptRank = map[string]int{
	ReceiptDurable:           1,
	ReceiptAttempted:         2,
	ReceiptTransportAccepted: 3,
	ReceiptContextObserved:   4,
	ReceiptApplicationAcked:  5,
}

// Receipt is the evidence a consumer holds about one record. Keyed by
// (MessageID, Recipient, Generation, Attempt): a receipt for parent A never
// acknowledges sender B's copy, and a restarted consumer (new Generation)
// starts its own attempts.
type Receipt struct {
	MessageID  string `json:"id"`
	Recipient  string `json:"to"`
	Generation int64  `json:"gen"`
	Attempt    int    `json:"attempt"`
	State      string `json:"state"`
	At         int64  `json:"at"` // Unix ms on the observing host
	Via        string `json:"via,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// ErrReceiptRegress is returned when a receipt would move to weaker
// evidence within the same attempt.
var ErrReceiptRegress = errors.New("comms: receipt state cannot move to weaker evidence")

// Advance returns the receipt moved to state at the given time. Within an
// attempt evidence only strengthens; ReceiptFailed is allowed from any
// state and ends the attempt; ReceiptUnknown is allowed from attempted
// only, and from unknown only context_observed, application_acked or
// failed follow.
func (r Receipt) Advance(state string, at int64) (Receipt, error) {
	if state == ReceiptFailed {
		r.State, r.At = state, at
		return r, nil
	}
	if r.State == ReceiptFailed {
		return r, errors.New("comms: attempt already failed; start a new attempt")
	}
	if state == ReceiptUnknown {
		if r.State != ReceiptAttempted {
			return r, errors.New("comms: unknown is reachable only from attempted")
		}
		r.State, r.At = state, at
		return r, nil
	}
	rank, ok := receiptRank[state]
	if !ok {
		return r, fmt.Errorf("comms: unknown receipt state %q", state)
	}
	if r.State == ReceiptUnknown {
		if state != ReceiptContextObserved && state != ReceiptApplicationAcked {
			return r, ErrReceiptRegress // a timeout or a transport report is not landing evidence
		}
		r.State, r.At = state, at
		return r, nil
	}
	if r.State != "" && rank <= receiptRank[r.State] {
		return r, ErrReceiptRegress
	}
	r.State, r.At = state, at
	return r, nil
}

// Retry starts the next attempt from ReceiptDurable.
func (r Receipt) Retry(at int64) Receipt {
	return Receipt{MessageID: r.MessageID, Recipient: r.Recipient, Generation: r.Generation,
		Attempt: r.Attempt + 1, State: ReceiptDurable, At: at}
}

// MaxAutoWakes caps the automatic wakes a parent gets without a human or a
// turn of its own in between (rule 3); the P2 wake path enforces it and
// records the pause as an `error` record addressed to the parent.
const MaxAutoWakes = 20

// MaxSparseAcks bounds the sparse acknowledgement set: a consumer that
// acknowledges records far ahead of a stuck one is told to deal with the
// stuck one (ErrAckWindow) rather than growing the set without bound.
const MaxSparseAcks = 4096

// ConsumerState is what a consumer keeps on disk
// (<ledger>/cursors/<consumer>.json): everything at or below Watermark is
// acknowledged; Acked lists acknowledged cursors above it (sparse, sorted);
// Epoch is the ledger epoch the cursors belong to (a different epoch means
// the ledger was reset or restored and the state must be rebuilt, never
// trusted); Generation counts restarts of the consumer so receipts from an
// older run are told apart.
type ConsumerState struct {
	Consumer   string          `json:"consumer"`
	Store      string          `json:"store,omitempty"`
	Epoch      int64           `json:"epoch,omitempty"`
	Generation int64           `json:"gen,omitempty"`
	Watermark  events.Cursor   `json:"watermark"`
	Acked      []events.Cursor `json:"acked,omitempty"`
}

// ErrAckWindow is returned by Ack when the sparse set is full.
var ErrAckWindow = errors.New("comms: too many unacknowledged records below the newest acknowledgement")

// ErrEpoch is returned by Check when the state belongs to another ledger
// epoch or store.
var ErrEpoch = errors.New("comms: consumer state belongs to another ledger epoch; rebuild it")

// Ack marks one cursor acknowledged and collapses the sparse set into the
// watermark where it is contiguous. Acknowledging a cursor at or below the
// watermark is a no-op.
func (c *ConsumerState) Ack(cursor events.Cursor) error {
	if cursor <= c.Watermark {
		return nil
	}
	i := sort.Search(len(c.Acked), func(i int) bool { return c.Acked[i] >= cursor })
	if i < len(c.Acked) && c.Acked[i] == cursor {
		return nil
	}
	if len(c.Acked) >= MaxSparseAcks && cursor != c.Watermark+1 {
		return ErrAckWindow // the stuck record (watermark + 1) is always accepted
	}
	c.Acked = append(c.Acked, 0)
	copy(c.Acked[i+1:], c.Acked[i:])
	c.Acked[i] = cursor
	c.normalize()
	return nil
}

// normalize sorts and dedups the sparse set, drops entries at or below the
// watermark (stale after a load or a merge) and moves the contiguous prefix
// into the watermark.
func (c *ConsumerState) normalize() {
	sort.Slice(c.Acked, func(i, j int) bool { return c.Acked[i] < c.Acked[j] })
	kept := c.Acked[:0]
	for i, a := range c.Acked {
		if a <= c.Watermark || (i > 0 && a == c.Acked[i-1]) {
			continue
		}
		kept = append(kept, a)
	}
	c.Acked = kept
	for len(c.Acked) > 0 && c.Acked[0] == c.Watermark+1 {
		c.Watermark = c.Acked[0]
		c.Acked = c.Acked[1:]
	}
	if len(c.Acked) == 0 {
		c.Acked = nil
	}
}

// SkipSpent applies the gap rule for spent cursors: a cursor that no
// record carries (a malformed line, a rolled-back commit, a frame that is
// not a record) is never delivered, so it can never be acknowledged; once
// every record below it is acknowledged the watermark moves over it, so the
// watermark, the sparse set and RetainFrom never stick on it. read must be
// every record one Export pass returned for (after, through], with through
// the cursor that pass returned. A pass that started
// above the watermark says nothing about the cursors below its start, so
// it changes nothing.
func (c *ConsumerState) SkipSpent(after events.Cursor, read []Exported, through events.Cursor) {
	if after > c.Watermark {
		return
	}
	carried := make(map[events.Cursor]bool, len(read))
	for _, e := range read {
		carried[e.Cursor] = true
	}
	for c.Watermark < through {
		next := c.Watermark + 1
		if carried[next] && !c.IsAcked(next) {
			break // a record still pending
		}
		c.Watermark = next
	}
	c.normalize()
}

// Normalize repairs a state read from disk (sorts, dedups, drops stale
// acks, folds the contiguous prefix). Call it after loading.
func (c *ConsumerState) Normalize() { c.normalize() }

// IsAcked reports whether a cursor is acknowledged.
func (c ConsumerState) IsAcked(cursor events.Cursor) bool {
	if cursor <= c.Watermark {
		return true
	}
	i := sort.Search(len(c.Acked), func(i int) bool { return c.Acked[i] >= cursor })
	return i < len(c.Acked) && c.Acked[i] == cursor
}

// Pending filters records to the ones this consumer has not acknowledged:
// the read position is the watermark, and a record above it is pending
// unless it is in the sparse set. An urgent record acknowledged ahead never
// hides an earlier info record.
func (c ConsumerState) Pending(records []Exported) []Exported {
	var out []Exported
	for _, e := range records {
		if !c.IsAcked(e.Cursor) {
			out = append(out, e)
		}
	}
	return out
}

// Check verifies the state belongs to the ledger it is used against.
func (c ConsumerState) Check(store StoreIdentity) error {
	if c.Store != "" && (c.Store != store.ID || c.Epoch != store.Epoch) {
		return ErrEpoch
	}
	return nil
}

// RetainFrom returns the lowest cursor that compaction must keep for a set
// of consumers: one above the lowest watermark. Pending-delivery retention
// is this bound; audit retention (RetentionDays) is the other, and a
// record is dropped only when both allow it. A consumer whose watermark
// has fallen out of the retained log is reported as a gap by Gap, never
// silently restarted at the newest segment.
func RetainFrom(states []ConsumerState) events.Cursor {
	if len(states) == 0 {
		return 0
	}
	low := states[0].Watermark
	for _, s := range states[1:] {
		if s.Watermark < low {
			low = s.Watermark
		}
	}
	return low + 1
}

// Gap describes records a consumer can no longer read because the log
// retained from oldest does not reach its watermark. The consumer records
// it as an explicit gap (an error record addressed to itself) before it
// moves its watermark to oldest-1.
type Gap struct {
	Consumer string        `json:"consumer"`
	From     events.Cursor `json:"from"` // first missing cursor (watermark + 1)
	To       events.Cursor `json:"to"`   // last missing cursor (oldest retained - 1)
}

// GapFor reports the gap between a consumer's watermark and the oldest
// retained cursor, if any.
func GapFor(c ConsumerState, oldestRetained events.Cursor) (Gap, bool) {
	if oldestRetained == 0 || c.Watermark+1 >= oldestRetained {
		return Gap{}, false
	}
	return Gap{Consumer: c.Consumer, From: c.Watermark + 1, To: oldestRetained - 1}, true
}
