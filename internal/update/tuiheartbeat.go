package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TUI heartbeats: every running TUI writes <cache>/tui/<pid>.json so the
// fleet watch (`agent-deck update --check --json`) can see which TUIs still
// run an older image than the file on disk, and why they have not restarted
// (2026-09-19: two TUIs kept running 1.16.11 and an rc build long after
// 1.16.12 landed, and nothing outside those processes could tell).

// TUIHeartbeatDirName is the cache-dir subdirectory holding one file per TUI.
const TUIHeartbeatDirName = "tui"

// TUIHeartbeatEvery is how often a TUI rewrites its heartbeat.
const TUIHeartbeatEvery = 30 * time.Second

// TUIHeartbeatStaleAfter is how old a heartbeat may be before its process,
// alive or not, counts as not reporting (a hung TUI).
const TUIHeartbeatStaleAfter = 5 * time.Minute

// TUIHeartbeat is what one TUI says about itself.
type TUIHeartbeat struct {
	PID       int       `json:"pid"`
	Version   string    `json:"version"`
	Exe       string    `json:"exe,omitempty"`
	Profile   string    `json:"profile,omitempty"`
	StartedAt time.Time `json:"started_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// LastTickAt is the last time the Bubble Tea loop ran a tick; while
	// attached to a session (tea.Exec) it stands still, and so does every
	// auto-update decision the loop makes.
	LastTickAt time.Time `json:"last_tick_at"`
	// InstalledVersion is the newer version the TUI has seen on disk, ""
	// when the file still matches the running build (or was never probed).
	InstalledVersion string `json:"installed_version,omitempty"`
	// InstalledSince is when InstalledVersion was first seen.
	InstalledSince time.Time `json:"installed_since,omitempty"`
	// RestartState is one of "idle", "waiting", "requested", "overdue".
	RestartState string `json:"restart_state,omitempty"`
	// BlockReason is why the restart has not happened (RestartState waiting
	// or overdue).
	BlockReason string `json:"block_reason,omitempty"`
}

// TUIReport is a heartbeat as `update --check --json` reports it.
type TUIReport struct {
	PID              int    `json:"pid"`
	Version          string `json:"version"`
	Profile          string `json:"profile,omitempty"`
	Outdated         bool   `json:"outdated"`
	InstalledVersion string `json:"installed_version,omitempty"`
	// Ticking is false when the loop has not ticked for a while: attached
	// to a session, or hung. Nothing auto-update does runs meanwhile.
	Ticking      bool   `json:"ticking"`
	RestartState string `json:"restart_state,omitempty"`
	BlockReason  string `json:"block_reason,omitempty"`
	// Since is how long the newer version has been on disk, in seconds.
	OutdatedForSeconds int64  `json:"outdated_for_seconds,omitempty"`
	StartedAt          string `json:"started_at"`
	UpdatedAt          string `json:"updated_at"`
}

func tuiHeartbeatPath(dir string, pid int) string {
	return filepath.Join(dir, TUIHeartbeatDirName, strconv.Itoa(pid)+".json")
}

// WriteTUIHeartbeat writes hb atomically under dir.
func WriteTUIHeartbeat(dir string, hb TUIHeartbeat) error {
	if hb.PID <= 0 {
		return errors.New("heartbeat without a pid")
	}
	path := tuiHeartbeatPath(dir, hb.PID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(hb, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data, 0o644)
}

// RemoveTUIHeartbeat deletes the file for pid; a missing file is fine.
func RemoveTUIHeartbeat(dir string, pid int) error {
	err := os.Remove(tuiHeartbeatPath(dir, pid))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// ListTUIHeartbeats reads every heartbeat under dir whose process alive
// says is still running; files of dead processes are removed. Sorted by pid.
func ListTUIHeartbeats(dir string, alive func(pid int) bool) ([]TUIHeartbeat, error) {
	entries, err := os.ReadDir(filepath.Join(dir, TUIHeartbeatDirName))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []TUIHeartbeat
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSuffix(name, ".json"))
		if err != nil || pid <= 0 {
			continue
		}
		path := filepath.Join(dir, TUIHeartbeatDirName, name)
		if !alive(pid) {
			_ = os.Remove(path)
			continue
		}
		data, err := os.ReadFile(path) // #nosec G304 -- fixed dir, numeric name
		if err != nil {
			continue
		}
		var hb TUIHeartbeat
		if err := json.Unmarshal(data, &hb); err != nil || hb.PID != pid {
			continue
		}
		out = append(out, hb)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out, nil
}

// ReportTUIs turns heartbeats into the report `update --check --json`
// prints: outdated is "running something older than onDisk" (a local build
// of the same number is not outdated), ticking is "ticked recently".
func ReportTUIs(hbs []TUIHeartbeat, onDisk string, now time.Time) []TUIReport {
	out := make([]TUIReport, 0, len(hbs))
	for _, hb := range hbs {
		r := TUIReport{
			PID:              hb.PID,
			Version:          hb.Version,
			Profile:          hb.Profile,
			Outdated:         onDisk != "" && CompareVersions(hb.Version, onDisk) < 0,
			InstalledVersion: hb.InstalledVersion,
			Ticking:          !hb.LastTickAt.IsZero() && now.Sub(hb.LastTickAt) < TUIHeartbeatStaleAfter,
			RestartState:     hb.RestartState,
			BlockReason:      hb.BlockReason,
			StartedAt:        hb.StartedAt.Format(time.RFC3339),
			UpdatedAt:        hb.UpdatedAt.Format(time.RFC3339),
		}
		if r.Outdated && !hb.InstalledSince.IsZero() {
			r.OutdatedForSeconds = int64(now.Sub(hb.InstalledSince).Seconds())
		}
		out = append(out, r)
	}
	return out
}

// DescribeTUIReport is the one-line human form of a report entry.
func DescribeTUIReport(r TUIReport) string {
	state := "current"
	if r.Outdated {
		state = "outdated"
	}
	line := fmt.Sprintf("pid %d v%s (%s", r.PID, r.Version, state)
	if !r.Ticking {
		line += ", not ticking: attached or hung"
	}
	if r.RestartState != "" && r.RestartState != "idle" {
		line += ", restart " + r.RestartState
		if r.BlockReason != "" {
			line += ": " + r.BlockReason
		}
	}
	return line + ")"
}
