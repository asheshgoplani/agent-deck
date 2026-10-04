package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
)

// Inbox statistics (issue #2469, design principle 7: measurable). Flat
// per-parent counters so the efficiency of the comms path is observable with
// `agent-deck inbox stats --json`: how often the parent was woken, how many
// turns were suppressed as noise or dedup, how many bytes were injected.
//
// Layout: one small JSON file per parent, <data>/runtime/inbox-stats/<parent>.json,
// rewritten durably on every bump. Bumps are rare (one per child turn) and the
// file is a few hundred bytes.

// InboxStats are the counters kept per parent. All are monotonic since
// StartedAt except the latency sample.
type InboxStats struct {
	Parent    string    `json:"parent"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// Producer side. Records* count records committed (to the parent's
	// inbox or the unowned ledger), not turns observed (issue #2481).
	RecordsUrgent   int64 `json:"records_urgent"`
	RecordsInfo     int64 `json:"records_info"`
	RecordsLegacy   int64 `json:"records_legacy"` // records without a tier (old producer / no transcript)
	NoiseSuppressed int64 `json:"noise_suppressed"`
	DedupSuppressed int64 `json:"dedup_suppressed"`
	// DoneRepeats counts identical completion sentinels a finished child
	// re-printed inside doneRepeatWindow (issue #2481): counted, never
	// committed, never woken.
	DoneRepeats int64 `json:"done_repeats"`
	TextBytes   int64 `json:"text_bytes"` // child text carried on records

	// Wake side.
	WakeupsUrgent     int64 `json:"wakeups_urgent"`
	WakeupsDigest     int64 `json:"wakeups_digest"`
	WakeupsSuppressed int64 `json:"wakeups_suppressed"` // info records that did not wake

	// Consumer side.
	Drains           int64 `json:"drains"`
	RecordsDelivered int64 `json:"records_delivered"`
	BytesInjected    int64 `json:"bytes_injected"` // Stop-block / context / nudge text
	FleetBlockSkips  int64 `json:"fleet_block_skips"`
	// ShadowedByLedger counts inbox records retired unshown because the
	// shared shown-turn set says the same turn was already delivered to this
	// parent by either path ([comms] consumers, exact turn identity).
	ShadowedByLedger int64 `json:"shadowed_by_ledger,omitempty"`

	// Latency: last urgent record commit -> delivery, in milliseconds.
	LastUrgentLatencyMS int64 `json:"last_urgent_latency_ms,omitempty"`
}

var inboxStatsMu sync.Mutex

// InboxStatsDir is the stats root.
func InboxStatsDir() string {
	return runtimeDirOrTemp("inbox-stats")
}

func inboxStatsPath(parentID string) string {
	return filepath.Join(InboxStatsDir(), sanitizeInboxName(parentID)+".json")
}

// ReadInboxStats returns the counters for one parent (zero values when none).
func ReadInboxStats(parentID string) (InboxStats, error) {
	inboxStatsMu.Lock()
	defer inboxStatsMu.Unlock()
	st, _, err := readInboxStatsFile(parentID, false)
	return st, err
}

// readInboxStatsFile returns the parent's counters plus the file's raw
// top-level fields, so a rewrite can carry the fields this binary does not
// know (issue #2481 item 7: a counter added by a newer agent-deck must survive
// a bump from an older one still running on the same host, and the other way
// round). A field of an unexpected type keeps the counters that did decode. A
// file that is not a JSON object at all reads as zero counters; with
// quarantine (the writer, holding the file lock) it is set aside as
// <file>.corrupt instead of being silently overwritten.
func readInboxStatsFile(parentID string, quarantine bool) (InboxStats, map[string]json.RawMessage, error) {
	st := InboxStats{Parent: strings.TrimSpace(parentID)}
	path := inboxStatsPath(parentID)
	data, err := os.ReadFile(path) // #nosec G304 -- sanitized id under the data dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil, nil
		}
		return st, nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		if quarantine {
			_ = os.Rename(path, path+".corrupt") // keep the evidence; start over
		}
		return st, nil, nil
	}
	var typeErr *json.UnmarshalTypeError
	if err := json.Unmarshal(data, &st); err != nil && !errors.As(err, &typeErr) {
		return InboxStats{Parent: strings.TrimSpace(parentID)}, raw, nil
	}
	return st, raw, nil
}

// marshalInboxStats encodes st over raw: every field InboxStats owns is
// replaced (or dropped when omitempty and zero), every other field in raw is
// kept as it was.
func marshalInboxStats(st InboxStats, raw map[string]json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return json.Marshal(st)
	}
	data, err := json.Marshal(st)
	if err != nil {
		return nil, err
	}
	var own map[string]json.RawMessage
	if err := json.Unmarshal(data, &own); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage)
	for k, v := range raw {
		if !inboxStatsFields[k] {
			out[k] = v
		}
	}
	for k, v := range own {
		out[k] = v
	}
	return json.Marshal(out)
}

// inboxStatsFields is the set of JSON keys InboxStats owns.
var inboxStatsFields = func() map[string]bool {
	t := reflect.TypeOf(InboxStats{})
	fields := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}()

// ListInboxStats returns every parent's counters, sorted by parent id.
func ListInboxStats() ([]InboxStats, error) {
	entries, err := os.ReadDir(InboxStatsDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []InboxStats
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		st, err := ReadInboxStats(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Parent < out[j].Parent })
	return out, nil
}

// BumpInboxStats applies fn to the parent's counters and persists them.
// Best-effort: callers ignore the error, since stats must never gate delivery.
func BumpInboxStats(parentID string, fn func(*InboxStats)) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	inboxStatsMu.Lock()
	defer inboxStatsMu.Unlock()
	// Counters live next to child text in runtime/; keep them owner-only.
	if err := os.MkdirAll(InboxStatsDir(), 0o700); err != nil {
		return err
	}
	// The daemon and every hook process bump the same file: without the
	// cross-process lock the last read-modify-write wins and the others'
	// increments are lost.
	lock, err := AcquireConfigFileLock(inboxStatsPath(parentID))
	if err != nil {
		return err
	}
	defer lock.Release()
	st, raw, err := readInboxStatsFile(parentID, true)
	if err != nil {
		return err
	}
	now := time.Now()
	if st.StartedAt.IsZero() {
		st.StartedAt = now
	}
	fn(&st)
	st.UpdatedAt = now
	data, err := marshalInboxStats(st, raw)
	if err != nil {
		return err
	}
	return writeFileDurable(inboxStatsPath(parentID), data, 0o600)
}

// ResetInboxStats removes a parent's counters.
func ResetInboxStats(parentID string) error {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return nil
	}
	inboxStatsMu.Lock()
	defer inboxStatsMu.Unlock()
	lock, err := AcquireConfigFileLock(inboxStatsPath(parentID))
	if err != nil {
		return err
	}
	defer lock.Release()
	err = os.Remove(inboxStatsPath(parentID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// statsParentFor picks the stats bucket for a child's turn before parent
// resolution has run: the child's registered parent, else the unowned ledger.
func statsParentFor(inst *Instance) string {
	if inst == nil || strings.TrimSpace(inst.ParentSessionID) == "" {
		return UnownedInboxID
	}
	return inst.ParentSessionID
}
