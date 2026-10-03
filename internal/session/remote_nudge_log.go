package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// The last nudge outcome per remote (#2472). The controller's
// auto_update_remotes nudge used to leave its result only in
// auto-update.log, so a nudge that died on `ssh ... Operation timed out` or
// `signal: killed` was invisible unless someone read the log. Each pass now
// records, per remote, the version it asked for and how it went in
// runtime/remote-nudges.json; `update --check --json` reports it as
// remote_nudges.

// RemoteNudgeRecord is one remote's latest nudge outcome.
type RemoteNudgeRecord struct {
	Remote       string `json:"remote"`
	AskedVersion string `json:"asked_version"`
	OK           bool   `json:"ok"`
	// Outcome is nudged | fallback | failed (NudgeResult's three cases).
	Outcome string    `json:"outcome"`
	Error   string    `json:"error,omitempty"`
	At      time.Time `json:"at"`
}

type remoteNudgeFile struct {
	Remotes map[string]RemoteNudgeRecord `json:"remotes"`
}

// remoteNudgeLockWait bounds how long a recorder waits for another one.
const remoteNudgeLockWait = 5 * time.Second

// RemoteNudgesPath is runtime/remote-nudges.json.
func RemoteNudgesPath() string {
	return runtimeDirOrTemp("remote-nudges.json")
}

// nudgeOutcomeName is the record's outcome word.
func nudgeOutcomeName(o NudgeOutcome) string {
	switch o {
	case NudgeOutcomeSent:
		return "nudged"
	case NudgeOutcomeFallback:
		return "fallback"
	}
	return "failed"
}

// RecordRemoteNudges merges one nudge pass into the record file: each
// remote's entry is replaced by this pass's outcome, other remotes keep
// theirs. Written durably under a file lock so two passes never interleave.
func RecordRemoteNudges(asked string, results []NudgeResult, at time.Time) error {
	if len(results) == 0 {
		return nil
	}
	path := RemoteNudgesPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	lock, err := AcquireConfigFileLockTimeout(path, remoteNudgeLockWait)
	if err != nil {
		return fmt.Errorf("lock %s: %w", filepath.Base(path), err)
	}
	defer lock.Release()
	f := readRemoteNudgeFile(path)
	for _, r := range results {
		rec := RemoteNudgeRecord{Remote: r.Name, AskedVersion: asked, Outcome: nudgeOutcomeName(r.Outcome), At: at.UTC()}
		rec.OK = r.Outcome != NudgeOutcomeFailed && r.Err == nil
		if r.Err != nil {
			rec.Error = r.Err.Error()
		}
		f.Remotes[r.Name] = rec
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return writeFileDurable(path, append(data, '\n'), 0o644)
}

// LoadRemoteNudges returns every remote's latest nudge outcome, sorted by
// remote. A missing or unreadable file is no records.
func LoadRemoteNudges() []RemoteNudgeRecord {
	f := readRemoteNudgeFile(RemoteNudgesPath())
	out := make([]RemoteNudgeRecord, 0, len(f.Remotes))
	for _, rec := range f.Remotes {
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Remote < out[j].Remote })
	return out
}

func readRemoteNudgeFile(path string) remoteNudgeFile {
	var f remoteNudgeFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &f)
	}
	if f.Remotes == nil {
		f.Remotes = map[string]RemoteNudgeRecord{}
	}
	return f
}
