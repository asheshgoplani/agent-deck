package session

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The writer-liveness heartbeat, for FINDING A of the 2026-08-20 field test.
//
// `remote drain` pulls records that only the notify-daemon ever writes. When no
// daemon is running on the remote there are no records — and the drain reported
// that as "the remote holds no completion or transition records", which is
// indistinguishable from a genuinely quiet host. An operator reads that as "all
// caught up" when the truth is "nothing has been watching". Three field rounds
// were spent on that ambiguity.
//
// So the daemon stamps a heartbeat once per pass, and the drain reports the
// absence explicitly. This is deliberately a plain mtime-bearing file rather
// than a pid or a lock: it answers "is something writing records right now",
// which is the question the drain actually needs, and it stays correct when the
// daemon is alive but wedged — a stale heartbeat is as useful a warning as a
// missing one.
const notifyHeartbeatFile = "notify-daemon.heartbeat"

// NotifyHeartbeatStaleAfter is how long without a stamp before the writer is
// reported as not running. The daemon's slowest poll is notifyPollSlow (3s), so
// this is two orders of magnitude of headroom: it never fires for a busy daemon,
// and a daemon down for a minute is genuinely not writing anything.
const NotifyHeartbeatStaleAfter = 90 * time.Second

func notifyHeartbeatPath() (string, error) {
	return runtimeDataPath(notifyHeartbeatFile)
}

// WriteNotifyHeartbeat stamps the current time. Best effort by contract: a
// heartbeat failure must never interfere with delivery, which is the daemon's
// actual job.
func WriteNotifyHeartbeat() {
	path, err := notifyHeartbeatPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// WriterStatus is what a drain needs to know about the remote's writer before it
// interprets an empty result.
type WriterStatus struct {
	Running       bool      `json:"running"`
	LastHeartbeat time.Time `json:"last_heartbeat,omitempty"`
	AgeSeconds    int64     `json:"age_seconds,omitempty"`
	// Detail is a human sentence for the CLI to print verbatim, so the wording
	// of the warning lives in one place rather than being reassembled by every
	// caller.
	Detail string `json:"detail"`
}

// ReadWriterStatus reports whether a notify-daemon is currently writing records
// on this host.
func ReadWriterStatus() WriterStatus {
	path, err := notifyHeartbeatPath()
	if err != nil {
		return WriterStatus{Detail: "cannot resolve the heartbeat path on this host"}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return WriterStatus{Detail: "the heartbeat file is unreadable; writer liveness is unknown"}
		}
		return WriterStatus{
			Detail: "no notify-daemon has ever stamped a heartbeat on this host — nothing is recording session transitions, so an empty drain says nothing about whether sessions finished. Start one with `agent-deck notify-daemon`.",
		}
	}
	stamp, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(raw)))
	if perr != nil {
		if info, serr := os.Stat(path); serr == nil {
			stamp = info.ModTime()
		} else {
			return WriterStatus{Detail: "the heartbeat file is unreadable; writer liveness is unknown"}
		}
	}
	age := time.Since(stamp)
	if age <= NotifyHeartbeatStaleAfter {
		return WriterStatus{
			Running:       true,
			LastHeartbeat: stamp.UTC(),
			AgeSeconds:    int64(age.Seconds()),
			Detail:        "a notify-daemon is running and recording transitions",
		}
	}
	return WriterStatus{
		LastHeartbeat: stamp.UTC(),
		AgeSeconds:    int64(age.Seconds()),
		Detail:        "the notify-daemon last stamped a heartbeat " + age.Truncate(time.Second).String() + " ago — it is not running, or it is wedged. Nothing is recording session transitions, so an empty drain says nothing about whether sessions finished.",
	}
}
