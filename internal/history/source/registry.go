package source

import (
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
)

type Live struct {
	PID        int
	SessionID  string
	CWD        string
	Name       string
	RawStatus  string
	WaitingFor string
	UpdatedMs  int64
}

// ReadRegistry reads ~/.claude/sessions/<pid>.json entries, keeps only those
// whose PID is still alive, and returns them keyed by session id.
func ReadRegistry(dir string) (map[string]Live, error) {
	out := map[string]Live{}
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || files == nil {
		return out, nil
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var d struct {
			PID        int    `json:"pid"`
			SessionID  string `json:"sessionId"`
			CWD        string `json:"cwd"`
			Name       string `json:"name"`
			Status     string `json:"status"`
			WaitingFor string `json:"waitingFor"`
			UpdatedAt  int64  `json:"updatedAt"`
		}
		if json.Unmarshal(data, &d) != nil || d.SessionID == "" || !IsAlive(d.PID) {
			continue
		}
		out[d.SessionID] = Live{PID: d.PID, SessionID: d.SessionID, CWD: d.CWD,
			Name: d.Name, RawStatus: d.Status, WaitingFor: d.WaitingFor, UpdatedMs: d.UpdatedAt}
	}
	return out, nil
}

// IsAlive reports whether a process with pid exists (signal 0 probe).
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return p.Signal(syscall.Signal(0)) == nil
}
