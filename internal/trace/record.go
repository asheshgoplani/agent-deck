package trace

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"time"
)

// SpoolVersion is the version of the SpoolRecord line format.
const SpoolVersion = 1

// Generic kinds a hook edge normalizes to. Ingest only looks at Kind; the
// raw hook event name is kept in Event for the events table.
const (
	KindSessionStart = "session_start"
	KindSessionEnd   = "session_end"
	KindTurnStart    = "turn_start"
	KindTurnEnd      = "turn_end"
	KindToolStart    = "tool_start"
	KindToolEnd      = "tool_end"
	KindWaitStart    = "wait_start" // permission prompt / elicitation: waiting for a human
	KindCompact      = "compact"
	KindOther        = "other"
)

// Span kinds and statuses (spans.kind, spans.status).
const (
	SpanTurn     = "turn"
	SpanToolCall = "tool_call"
	SpanSubagent = "subagent"
	SpanWait     = "wait"

	StatusOK    = "ok"
	StatusError = "error"
	StatusUnset = "unset"
)

// SpoolRecord is one normalized, redacted hook edge: one JSON line in the
// spool. Every string in it is either an id that passed cleanID, a fixed
// vocabulary value, or a summary that passed Scrub.
type SpoolRecord struct {
	V         int    `json:"v"`
	ID        string `json:"id"`   // unique per edge; the idempotency key at ingest
	At        int64  `json:"at"`   // unix ms when the hook received the edge
	Tool      string `json:"tool"` // agent tool: claude, codex, ...
	Event     string `json:"event"`
	Kind      string `json:"kind"`
	Profile   string `json:"profile,omitempty"`
	Instance  string `json:"instance,omitempty"` // agent-deck instance id
	SessionID string `json:"session_id,omitempty"`
	TurnID    string `json:"turn_id,omitempty"`
	Host      string `json:"host,omitempty"`

	ToolName  string `json:"tool_name,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Status    string `json:"status,omitempty"` // tool_end: ok | error
	Summary   string `json:"summary,omitempty"`
	Model     string `json:"model,omitempty"`
}

// TraceID is the trace a record belongs to: one trace per agent session.
// Falls back to the agent-deck instance when the harness sent no session id.
func (r SpoolRecord) TraceID() string {
	sid := r.SessionID
	if sid == "" {
		sid = "instance:" + r.Instance
	}
	return r.Tool + ":" + sid
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

// cleanID passes a harness-provided id through unchanged when it is
// id-shaped and contains nothing the scrubber would touch; anything else is
// replaced by a stable hash so it can still pair edges without carrying the
// original bytes.
func cleanID(s string) string {
	if s == "" {
		return ""
	}
	if idRe.MatchString(s) && Scrub(s) == s {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "h:" + hex.EncodeToString(sum[:8])
}

// newRecordID returns a unique id: time prefix (sortable) + random suffix.
func newRecordID(at time.Time) string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatInt(at.UnixNano(), 36) + "-" + hex.EncodeToString(b[:])
}
