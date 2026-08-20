package agents

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// HealthState is what we can actually prove about a connector or service.
type HealthState string

const (
	// HealthOK means we found positive evidence it is working: a live process,
	// or a freshness file touched inside the staleness window.
	HealthOK HealthState = "ok"
	// HealthStale means the evidence exists but is old. The thing may be
	// running and idle, or it may be wedged — the age is reported so a human
	// can tell.
	HealthStale HealthState = "stale"
	// HealthDown means we found positive evidence it is NOT working: a pid
	// file naming a process that is gone.
	HealthDown HealthState = "down"
	// HealthUnknown means we could not find evidence either way. It is not a
	// synonym for down, and must never be rendered as one.
	HealthUnknown HealthState = "unknown"
)

// DefaultStaleAfter is the age past which freshness evidence stops counting as
// proof of life. It is a display threshold, not a policy: the exact age is
// always reported alongside the state.
const DefaultStaleAfter = 30 * time.Minute

// Health is one connector or service row.
type Health struct {
	Name  string      `json:"name"`
	Kind  string      `json:"kind,omitempty"`
	State HealthState `json:"state"`
	// Detail is the human-readable reason for State, always naming the
	// evidence it rests on.
	Detail string `json:"detail"`
	// PID is set when a pid file was found and read.
	PID int `json:"pid,omitempty"`
	// LastFresh is the newest mtime found under the evidence path.
	LastFresh time.Time `json:"last_fresh,omitempty"`
	// EvidencePath is what was inspected.
	EvidencePath string `json:"evidence_path,omitempty"`
	// FreshnessFile is the specific file whose mtime produced LastFresh.
	FreshnessFile string `json:"freshness_file,omitempty"`
}

// freshnessNames are the file names that indicate a poller did work, in the
// order we prefer them. A seen-database that just grew is the strongest
// evidence a mail poller actually fetched.
var freshnessNames = []string{
	"seen.db", "seen.json", "seen", "state.json", "cursor", "cursor.json",
	"health", "health.json", "health.txt", "last-run", "last_run", "heartbeat",
}

// pidFileNames are where a poller or service typically records its pid.
var pidFileNames = []string{"pid", "run.pid", "daemon.pid", "service.pid"}

// CheckHealth inspects a connector or service and reports what the evidence
// supports. It reads mtimes and pid files, and probes process liveness with a
// null signal. It never starts, stops, or signals anything for real.
func CheckHealth(name, kind, evidencePath string, staleAfter time.Duration, now time.Time) Health {
	h := Health{Name: name, Kind: kind, EvidencePath: evidencePath, State: HealthUnknown}
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	if strings.TrimSpace(evidencePath) == "" {
		h.Detail = "no local evidence path is bound; health cannot be determined"
		return h
	}

	info, err := os.Stat(evidencePath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			h.Detail = "evidence path does not exist: " + evidencePath
			return h
		}
		h.Detail = "cannot read evidence path: " + err.Error()
		return h
	}

	// A pid file is the strongest signal, because it can prove the negative.
	if info.IsDir() {
		if pid, pidPath, found := readPIDFile(evidencePath); found {
			h.PID = pid
			if processAlive(pid) {
				h.State = HealthOK
				h.Detail = fmt.Sprintf("pid %d from %s is alive", pid, filepath.Base(pidPath))
			} else {
				h.State = HealthDown
				h.Detail = fmt.Sprintf("pid %d from %s is not running", pid, filepath.Base(pidPath))
				return h
			}
		}
	}

	freshFile, freshTime, found := newestFreshness(evidencePath, info)
	if !found {
		if h.State == HealthOK {
			// Live process, no freshness trace. Say exactly that.
			h.Detail += "; no freshness file found, so recent work is unproven"
		} else {
			h.Detail = "no pid file and no freshness file under " + evidencePath
		}
		return h
	}

	h.LastFresh = freshTime
	h.FreshnessFile = freshFile
	age := now.Sub(freshTime)
	ageText := fmt.Sprintf("%s ago", RoundDuration(age))
	if age < 0 {
		// A future timestamp is not proof of recent work, it is a clock
		// problem. RoundDuration takes an absolute value, so without this a
		// file dated next week would read as "5m ago" and count as fresh.
		h.State = HealthUnknown
		h.Detail = fmt.Sprintf("%s is dated %s in the future; clock or mtime is wrong",
			filepath.Base(freshFile), RoundDuration(age))
		return h
	}

	switch {
	case age <= staleAfter:
		if h.State != HealthOK {
			h.State = HealthOK
		}
		h.Detail = fmt.Sprintf("%s updated %s", filepath.Base(freshFile), ageText)
	default:
		if h.State == HealthOK {
			// Process is alive but has not done anything in a while. That is
			// stale work, not a dead service, and the row should say so.
			h.State = HealthStale
			h.Detail = fmt.Sprintf("pid %d alive but %s last updated %s", h.PID, filepath.Base(freshFile), ageText)
		} else {
			h.State = HealthStale
			h.Detail = fmt.Sprintf("%s last updated %s", filepath.Base(freshFile), ageText)
		}
	}
	return h
}

// readPIDFile looks for a pid file in a directory and reads it.
func readPIDFile(dir string) (int, string, bool) {
	for _, name := range pidFileNames {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var pid int
		if _, err := fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &pid); err != nil || pid <= 0 {
			continue
		}
		return pid, path, true
	}
	return 0, "", false
}

// processAlive probes a pid with signal 0: it asks the kernel whether the
// process exists without delivering anything.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	// EPERM means it exists and belongs to someone else.
	return errors.Is(err, os.ErrPermission) || errors.Is(err, syscall.EPERM)
}

// newestFreshness finds the most recent freshness evidence at a path. For a
// file, that is the file itself. For a directory, it prefers a known freshness
// name and otherwise falls back to the newest regular file directly inside it.
func newestFreshness(path string, info os.FileInfo) (string, time.Time, bool) {
	if !info.IsDir() {
		return path, info.ModTime(), true
	}

	for _, name := range freshnessNames {
		candidate := filepath.Join(path, name)
		if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
			return candidate, stat.ModTime(), true
		}
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return "", time.Time{}, false
	}
	var (
		newestPath string
		newestTime time.Time
		found      bool
	)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		stat, err := entry.Info()
		if err != nil {
			continue
		}
		if !found || stat.ModTime().After(newestTime) {
			newestPath = filepath.Join(path, entry.Name())
			newestTime = stat.ModTime()
			found = true
		}
	}
	return newestPath, newestTime, found
}

// RoundDuration renders an age the way the fleet views do: coarse, and never
// more precise than it is meaningful.
func RoundDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
