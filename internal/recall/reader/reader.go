// Package reader turns harness transcript files into a stream of decoded
// events. One interface, one file per harness, one registry line to add a
// new one. A reader never touches SQL and never sees the database: ingest
// (the only writer) owns the ledger, the cursors and the byte budget.
package reader

import (
	"context"
	"errors"
	"time"
)

// Root is one place a harness keeps transcripts: for Claude a config dir
// with a projects/ subtree. Profile is the agent-deck profile that owns it
// ("" when the dir is not mapped to one).
type Root struct {
	Harness       string
	Profile       string
	Dir           string
	RetentionDays int
}

// SourceRef is one discovered transcript file. Path is symlink-resolved and
// (Dev, Ino) is its identity, so the same file reached through 189
// worker-scratch symlinks is one source.
type SourceRef struct {
	Harness       string
	Profile       string
	Path          string
	Dev           uint64
	Ino           uint64
	Size          int64
	MtimeNS       int64
	RetentionDays int
	// NativeID is the harness conversation id derived from the path; the
	// reader may refine it from the file's own records.
	NativeID string
	// IsSidechain marks a subagent transcript; ParentNativeID is the owning
	// session and feeds the subagent_of edge.
	IsSidechain    bool
	ParentNativeID string
}

// Session carries what the reader learned about the conversation as a
// whole. Fields are zero when unknown; ingest merges non-zero values.
type Session struct {
	NativeID string
	CWD      string
	Branch   string
	Version  string
	Title    string
	TitleSrc string
	Model    string
}

// Msg is one indexable message: already-decoded text (text blocks only),
// never escaped JSON, so the phrase verifier and the snippet renderer see
// the same bytes.
type Msg struct {
	Role         int
	TS           int64
	RecOff       int64
	RecLen       int64
	Text         string
	UUID         string
	ToolNames    []string
	IsMeta       bool
	IsToolResult bool
	IsError      bool
	IsInterrupt  bool
	IsCompact    bool
}

// ToolCall is one tool_use, joined to its tool_result when the result was
// seen in the same pass (DurationMS 0 otherwise).
type ToolCall struct {
	Name       string
	TS         int64
	DurationMS int64
	IsError    bool
	ArgDigest  string
	Touches    []FileTouch
}

// FileTouch is a path a tool call read, wrote or edited.
type FileTouch struct {
	Path string
	Op   string
}

// Usage is one assistant record's token usage; ingest sums it into the
// session and, for a linked deck session, hands it to the cost store, so
// the transcript is read once, not twice.
type Usage struct {
	UUID   string
	TS     time.Time
	Model  string
	In     int64
	Out    int64
	CacheR int64
	CacheW int64
}

// Counter names the structural events that only bump a number.
type Counter int

const (
	CountCompact Counter = iota
	CountInterrupt
	CountAPIError
	CountUnknownType
	CountLineTooLong
	CountBadJSON
	CountTurnDurationMS
)

// Sink receives the decoded stream. Ingest implements it; tests use a
// recorder.
type Sink interface {
	Session(Session)
	Msg(Msg) error
	ToolCall(ToolCall) error
	Usage(Usage) error
	Count(Counter, int64)
}

// Reader is one harness.
type Reader interface {
	Harness() string
	// Discover walks the roots and emits every candidate source once.
	Discover(ctx context.Context, roots []Root, emit func(SourceRef) error) error
	// Ingest parses src from byte offset from and returns the offset of the
	// last complete record it handed to sink. It returns ErrBudget when the
	// budget ran out first; parsedTo is still valid then.
	Ingest(ctx context.Context, src SourceRef, from int64, sink Sink, b *Budget) (parsedTo int64, err error)
}

// ErrBudget means the pass stopped early because the Budget was exhausted.
var ErrBudget = errors.New("recall: budget exhausted")

// Budget bounds one pass in wall time and in bytes read. The zero Budget is
// unlimited.
type Budget struct {
	Deadline  time.Time
	BytesLeft int64
	limited   bool
	consumed  int64
	deferred  bool
}

// NewBudget returns a budget of d wall time and maxBytes bytes (either may
// be zero for unlimited).
func NewBudget(d time.Duration, maxBytes int64) *Budget {
	b := &Budget{BytesLeft: maxBytes, limited: d > 0 || maxBytes > 0}
	if d > 0 {
		b.Deadline = time.Now().Add(d)
	}
	return b
}

// Consume records n bytes read and reports whether the pass may continue.
func (b *Budget) Consume(n int64) bool {
	if b == nil {
		return true
	}
	b.consumed += n
	if !b.limited {
		return true
	}
	if b.BytesLeft > 0 {
		b.BytesLeft -= n
		if b.BytesLeft <= 0 {
			b.deferred = true
			return false
		}
	}
	return !b.Expired()
}

// Expired reports whether the wall deadline has passed.
func (b *Budget) Expired() bool {
	if b == nil || b.Deadline.IsZero() {
		return false
	}
	if time.Now().After(b.Deadline) {
		b.deferred = true
		return true
	}
	return false
}

// Exhausted reports whether Consume or Expired ever said stop.
func (b *Budget) Exhausted() bool { return b != nil && b.deferred }

// Consumed is the number of bytes charged so far.
func (b *Budget) Consumed() int64 {
	if b == nil {
		return 0
	}
	return b.consumed
}

// Registry lists the shipped readers; phase 3 adds the other harnesses.
func Registry() []Reader {
	return []Reader{Claude{}}
}
