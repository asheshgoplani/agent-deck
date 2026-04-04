package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionIDLifecycleEvent captures every session-ID bind/rebind/reject decision.
// Events are appended as JSONL for postmortem debugging.
type SessionIDLifecycleEvent struct {
	InstanceID string `json:"instance_id"`
	Tool       string `json:"tool"`
	Action     string `json:"action"` // bind | rebind | reject | scan_disabled
	Source     string `json:"source"` // tmux_env | hook_payload | hook_anchor | disk_scan
	OldID      string `json:"old_id,omitempty"`
	NewID      string `json:"new_id,omitempty"`
	Candidate  string `json:"candidate,omitempty"`
	Reason     string `json:"reason,omitempty"`
	HookEvent  string `json:"hook_event,omitempty"`
	Timestamp  int64  `json:"ts"`
}

var sessionIDLifecycleLogMu sync.Mutex

// GetSessionIDLifecycleLogPath returns ~/.agent-deck/logs/session-id-lifecycle.jsonl.
func GetSessionIDLifecycleLogPath() string {
	agentDeckDir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "logs", "session-id-lifecycle.jsonl")
	}
	return filepath.Join(agentDeckDir, "logs", "session-id-lifecycle.jsonl")
}

// WriteSessionIDLifecycleEvent appends a single JSONL event.
func WriteSessionIDLifecycleEvent(event SessionIDLifecycleEvent) error {
	if event.Timestamp == 0 {
		event.Timestamp = time.Now().Unix()
	}

	logPath := GetSessionIDLifecycleLogPath()
	if err := os.MkdirAll(filepath.Dir(logPath), 0755); err != nil {
		return fmt.Errorf("create lifecycle log dir: %w", err)
	}

	line, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal lifecycle event: %w", err)
	}
	line = append(line, '\n')

	sessionIDLifecycleLogMu.Lock()
	defer sessionIDLifecycleLogMu.Unlock()

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open lifecycle log: %w", err)
	}
	defer f.Close()

	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("write lifecycle event: %w", err)
	}
	return nil
}
