package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

	// Producer side.
	RecordsUrgent   int64 `json:"records_urgent"`
	RecordsInfo     int64 `json:"records_info"`
	RecordsLegacy   int64 `json:"records_legacy"` // records without a tier (old producer / no transcript)
	NoiseSuppressed int64 `json:"noise_suppressed"`
	DedupSuppressed int64 `json:"dedup_suppressed"`
	TextBytes       int64 `json:"text_bytes"` // child text carried on records

	// Wake side.
	WakeupsUrgent     int64 `json:"wakeups_urgent"`
	WakeupsDigest     int64 `json:"wakeups_digest"`
	WakeupsSuppressed int64 `json:"wakeups_suppressed"` // info records that did not wake

	// Consumer side.
	Drains           int64 `json:"drains"`
	RecordsDelivered int64 `json:"records_delivered"`
	BytesInjected    int64 `json:"bytes_injected"` // Stop-block / context / nudge text
	FleetBlockSkips  int64 `json:"fleet_block_skips"`

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
	return readInboxStatsLocked(parentID)
}

func readInboxStatsLocked(parentID string) (InboxStats, error) {
	st := InboxStats{Parent: strings.TrimSpace(parentID)}
	data, err := os.ReadFile(inboxStatsPath(parentID)) // #nosec G304 -- sanitized id under the data dir
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return InboxStats{Parent: strings.TrimSpace(parentID)}, nil // corrupt file: start over
	}
	return st, nil
}

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
	st, err := readInboxStatsLocked(parentID)
	if err != nil {
		return err
	}
	now := time.Now()
	if st.StartedAt.IsZero() {
		st.StartedAt = now
	}
	fn(&st)
	st.UpdatedAt = now
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	// Counters live next to child text in runtime/; keep them owner-only.
	if err := os.MkdirAll(InboxStatsDir(), 0o700); err != nil {
		return err
	}
	return writeFileDurable(inboxStatsPath(parentID), data, 0o600)
}

// ResetInboxStats removes a parent's counters.
func ResetInboxStats(parentID string) error {
	inboxStatsMu.Lock()
	defer inboxStatsMu.Unlock()
	err := os.Remove(inboxStatsPath(parentID))
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
