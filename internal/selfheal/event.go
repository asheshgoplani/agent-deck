package selfheal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ReadSig is one of the two confirming reads recorded in an event (§5). It pairs
// the read time with the stable output signature so a reviewer can verify the
// two reads agreed (the §1.3 #4 two-read confirm).
type ReadSig struct {
	T   string `json:"t"`
	Sig string `json:"sig"`
}

// Event is the single structured record emitted per detection cycle, whether or
// not it acts (§5). It is the observability hook for the future OGDB
// operational-KG (we emit the record; we do NOT build the sink). In observe mode
// Stage is "observe" and WouldHave carries the action that WOULD have run.
type Event struct {
	TS        string  `json:"ts"`
	SessionID string  `json:"session_id"`
	Title     string  `json:"title"`
	Conductor string  `json:"conductor,omitempty"`
	Group     string  `json:"group,omitempty"`
	Profile   string  `json:"profile"`
	Account   string  `json:"account,omitempty"`
	Substate  string  `json:"substate"`
	Dwell     float64 `json:"dwell_seconds"`

	Reads []ReadSig `json:"reads"`

	Decision Decision `json:"decision"`
	// Action is the action TAKEN (null/empty on skip and in observe mode — observe
	// never takes an action). WouldHave carries the observe-mode would-be action.
	Action       Action         `json:"action,omitempty"`
	ActionParams map[string]any `json:"action_params,omitempty"`
	Caps         CapsState      `json:"caps"`
	Outcome      string         `json:"outcome,omitempty"`
	Stage        Mode           `json:"stage"`
	// WouldHave is present in observe mode: the action self-heal WOULD have taken
	// had it been authorized. Empty when the decision was a skip.
	WouldHave Action `json:"would_have,omitempty"`
}

// EventSink is the durable audit destination. The default is an append-only
// NDJSON file; the operational-KG ingestion path can subscribe to the same
// records later (§5). Append must be safe for concurrent callers.
type EventSink interface {
	Append(Event) error
}

// NDJSONSink is an append-only newline-delimited-JSON file sink. It uses a
// targeted append (O_APPEND create), NEVER a full-file rewrite and NEVER
// SaveInstances — it cannot wipe or truncate existing audit history (§3.5: no
// destructive write primitive). The parent directory is created 0o700.
type NDJSONSink struct {
	path string
	mu   sync.Mutex
}

// NewNDJSONSink returns a sink appending to path. The directory is created if
// absent. The file itself is created lazily on first Append.
func NewNDJSONSink(path string) (*NDJSONSink, error) {
	if path == "" {
		return nil, fmt.Errorf("selfheal: empty audit path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("selfheal: mkdir audit dir: %w", err)
	}
	return &NDJSONSink{path: path}, nil
}

// Path returns the audit file path (so callers/tests can report where it lands).
func (s *NDJSONSink) Path() string { return s.path }

// Append writes one event as a single JSON line. O_APPEND guarantees the write
// only ever extends the file; it never truncates or overwrites prior records.
func (s *NDJSONSink) Append(e Event) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("selfheal: marshal event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("selfheal: open audit file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("selfheal: append audit line: %w", err)
	}
	return nil
}

// MemorySink collects events in memory (tests, and any in-process subscriber).
type MemorySink struct {
	mu     sync.Mutex
	Events []Event
}

// Append records the event.
func (m *MemorySink) Append(e Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Events = append(m.Events, e)
	return nil
}

// Snapshot returns a copy of the recorded events.
func (m *MemorySink) Snapshot() []Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Event, len(m.Events))
	copy(out, m.Events)
	return out
}

func formatTS(t time.Time) string { return t.UTC().Format(time.RFC3339) }
