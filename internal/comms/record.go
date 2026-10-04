// Package comms is the Comms Ledger: one append-only, daemon-written message
// log per profile with a cursor per consumer. It generalises the turn
// journal and the per-parent inbox of issue #2469 into one record schema and
// one store, fed by the hooks agent-deck already installs (the producer
// adapters forward the text they receive instead of discarding it) and read
// by `agent-deck msg`. The log itself is an internal/events bus opened at
// <data>/comms/<profile>/ with time-based retention; this package owns the
// record shape, the idempotency key and the render helpers.
//
// Rules: only the notify daemon writes the ledger (hooks spool, the daemon
// commits). Every record is a measurement row as well as a message: ts,
// kind, from, to, tool, origin, tier, trigger, bytes and the latency fields
// are stored on the line, noise included, so `msg stats` never needs a
// second store.
package comms

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
	"unicode/utf8"
)

// Record kinds.
const (
	KindTurn     = "turn"     // a child's finished turn, carrying its final text
	KindSend     = "send"     // a `session send` leaving the sender
	KindDelivery = "delivery" // a send's state change in the send queue
	KindWake     = "wake"     // a nudge typed into an idle parent
	KindHuman    = "human"    // a line bound for the human (conductor -> Telegram)
	KindError    = "error"    // a producer or delivery failure worth a record
	KindStatus   = "status"   // a status-only edge for tools with no text (shell)
	// KindCall is a measurement row: a session ran a read verb (`session
	// output`, `inbox drain`, `msg read`) that a ledger-fed parent should
	// not need. Never delivered.
	KindCall = "call"
)

// Tiers, as issue #2469 defines them.
const (
	TierUrgent = "urgent"
	TierInfo   = "info"
	TierNoise  = "noise"
)

// Delivery states of a send, delivery or wake record.
const (
	StateQueued   = "queued"
	StatePushed   = "pushed"
	StateInjected = "injected"
	StateTyped    = "typed"
	StateLanded   = "landed"
	StateFailed   = "failed"
)

// SchemaVersion is stamped on every record as "v". A reader that meets a
// higher version keeps the fields it knows; a writer never reuses a version
// for a different shape.
const SchemaVersion = 1

// Text caps: the child text carried on a record. MaxTextBytes is the hard
// ceiling whatever the config says.
const (
	DefaultTextBytes = 600
	MaxTextBytes     = 2048
)

// Record is one ledger line. Field names are short on purpose: the line is
// read back on every drain and every stats query. Timestamps are Unix
// milliseconds. All omitempty except the identity fields.
type Record struct {
	V       int      `json:"v"`                 // SchemaVersion of the record shape
	ID      string   `json:"id"`                // ULID, assigned at commit
	Key     string   `json:"key,omitempty"`     // idempotency key (producer-stable)
	Kind    string   `json:"kind"`              // Kind* constants
	From    string   `json:"from"`              // session id (or human:<name>)
	To      []string `json:"to,omitempty"`      // consumer session ids this is addressed to
	Profile string   `json:"profile,omitempty"` // source profile (the producer's)
	// ToProfile and ToStore name the destination: the profile the consumers
	// in To live in and the id of the ledger that holds the record for them
	// (the local ledger at ingest; the same as Store for a local record).
	ToProfile string `json:"to_profile,omitempty"`
	ToStore   string `json:"to_store,omitempty"`
	Tool      string `json:"tool,omitempty"`   // harness of From (claude, codex, ...)
	Host      string `json:"host,omitempty"`   // display only: the short hostname at commit time (mutable; never an identity)
	Store     string `json:"store,omitempty"`  // identity: id of the ledger the record was first committed to (stable across host renames)
	Epoch     int64  `json:"epoch,omitempty"`  // that ledger's epoch (bumped when its history is reset or restored)
	Origin    string `json:"origin,omitempty"` // configured remote name when the record was pulled from another host's ledger
	// SrcCursor is the record's cursor on the origin ledger (remote-first,
	// docs/comms.md): the importer advances its (remote, consumer) cursor to
	// the highest SrcCursor it committed, and a re-pull is idempotent on
	// Origin + Key.
	SrcCursor uint64 `json:"src_cursor,omitempty"`
	Tier      string `json:"tier,omitempty"`    // urgent | info | noise
	Trigger   string `json:"trigger,omitempty"` // human | send | task | system | inbox | unknown
	Text      string `json:"text,omitempty"`    // capped text
	TH        string `json:"th,omitempty"`      // text hash (sha256/16)
	Bytes     int    `json:"bytes,omitempty"`   // len(Text) at commit
	Q         bool   `json:"q,omitempty"`       // parent-facing question
	Done      string `json:"done,omitempty"`    // completion sentinel status (ok|fail)
	Summary   string `json:"summary,omitempty"` // completion sentinel summary
	Err       string `json:"err,omitempty"`     // error text for KindError
	Seq       int64  `json:"seq,omitempty"`     // per-From sequence
	Req       string `json:"req,omitempty"`     // caller request id of a send: the receipt is this record, written before the action
	ReplyTo   string `json:"reply_to,omitempty"`
	Via       string `json:"via,omitempty"`   // tmux | socket | ssh | telegram | hook
	State     string `json:"state,omitempty"` // State* constants
	Ref       string `json:"ref,omitempty"`   // id of the record this one is about
	// Refs are the ids of every record a wake or a delivery carried.
	Refs []string `json:"refs,omitempty"`

	TSignal   int64 `json:"t_signal,omitempty"` // the harness signal (hook) fired
	TRecord   int64 `json:"t_record,omitempty"` // the daemon committed the record
	TPushed   int64 `json:"t_pushed,omitempty"` // the record left for a consumer
	TSeen     int64 `json:"t_seen,omitempty"`   // a consumer's prompt carried it
	LatencyMS int64 `json:"latency_ms,omitempty"`

	// Cross-host timing of an imported record (P3), measured on the
	// importing host. TImport is the local commit. XLatencyMS estimates
	// the origin's signal (or commit) to TImport with the clock offset
	// between the hosts corrected; XErrMS is its uncertainty (half the
	// round trip that measured the offset plus clock granularity). An
	// estimate that the uncertainty could make negative is not stored: the
	// latency is then unknown, never a precise-looking wrong number.
	TImport    int64 `json:"t_import,omitempty"`
	XLatencyMS int64 `json:"xlat_ms,omitempty"`
	XErrMS     int64 `json:"xlat_err_ms,omitempty"`
}

// DedupKey is what the idempotency window is keyed on: the producer's key
// namespaced by the ORIGIN STORE ID for a pulled record (the remote alias
// is display only and may be renamed), so two hosts' children can never
// collide and a re-pulled remote record is still dropped.
func (r Record) DedupKey() string {
	if r.Key == "" {
		return ""
	}
	if r.Origin == "" {
		return r.Key
	}
	return r.Store + "|" + r.Key
}

// ContentHash identifies what a record says, independent of when it was
// committed: a second commit under the same key with a different content
// hash is a conflict, not a duplicate.
func (r Record) ContentHash() string {
	text := r.TH
	if text == "" {
		text = TextHash(r.Text)
	}
	return TextHash(strings.Join([]string{r.Kind, r.From, strings.Join(r.To, ","), text, r.State, r.Done, r.Summary, r.Err}, "\x00"))
}

// IsUrgent reports whether a consumer must wake for this record: an urgent
// tier, or no tier at all (a status-only record from a tool without text).
func (r Record) IsUrgent() bool { return r.Tier == "" || r.Tier == TierUrgent }

// Stamp fills what every committed record carries: an id, the commit time,
// the text hash and byte count. Idempotent on an already stamped record.
func (r *Record) Stamp(now time.Time) {
	if r.V == 0 {
		r.V = SchemaVersion
	}
	if r.ID == "" {
		r.ID = NewID(now)
	}
	if r.TRecord == 0 {
		r.TRecord = now.UnixMilli()
	}
	if r.TH == "" && r.Text != "" {
		r.TH = TextHash(r.Text)
	}
	r.Bytes = len(r.Text)
	if r.LatencyMS == 0 && r.TSignal > 0 && r.TRecord >= r.TSignal {
		r.LatencyMS = r.TRecord - r.TSignal
	}
}

// TextHash is the first 16 hex of sha256(text), "" for empty text. It is the
// same function the turn journal uses, so hashes line up across stores.
func TextHash(text string) string {
	if text == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])[:16]
}

// SendKey is the idempotency key of a send with a caller request id: a
// retry with the same id finds the stored receipt (the send record) instead
// of causing a second delivery.
func SendKey(from, req string) string { return Key(KindSend, from, req) }

// Key returns the idempotency key for a record a producer may observe more
// than once: "<kind>:<from>:<sha256/16 of the parts>". The daemon drops a
// second commit with the same key; a remote host's export carries the key so
// a re-pulled record is committed once.
func Key(kind, from string, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return kind + ":" + from + ":" + hex.EncodeToString(sum[:])[:16]
}

// CapText truncates text to at most max bytes on a rune boundary, marking
// the clip. max <= 0 means DefaultTextBytes; MaxTextBytes is the ceiling.
func CapText(text string, max int) string {
	if max <= 0 {
		max = DefaultTextBytes
	}
	if max > MaxTextBytes {
		max = MaxTextBytes
	}
	if len(text) <= max {
		return text
	}
	const marker = "…"
	keep := max - len(marker)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(text[keep]) {
		keep--
	}
	return text[:keep] + marker
}
