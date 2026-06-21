package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"
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

var (
	sessionIDLifecycleLogMu sync.Mutex
	// sessionIDLifecycleWriter is a lazily-initialised rotating writer. Before
	// this, WriteSessionIDLifecycleEvent was a raw O_APPEND with no cap and had
	// grown to ~22MB / 92K lines on the live mac-studio. lumberjack bounds it the
	// same way debug.log is bounded (size + backups + age). Guarded by
	// sessionIDLifecycleLogMu.
	sessionIDLifecycleWriter *lumberjack.Logger
)

// GetSessionIDLifecycleLogPath returns ~/.agent-deck/logs/session-id-lifecycle.jsonl.
func GetSessionIDLifecycleLogPath() string {
	path, err := logDataPath("session-id-lifecycle.jsonl")
	if err != nil {
		return tempAgentDeckPath("logs", "session-id-lifecycle.jsonl")
	}
	return path
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

	// Lazily bind the rotating writer to the resolved path. lumberjack creates
	// the file and rotates it in place (5MB × 3 backups, 30-day retention) so
	// this postmortem log can never grow unbounded again.
	if sessionIDLifecycleWriter == nil || sessionIDLifecycleWriter.Filename != logPath {
		if sessionIDLifecycleWriter != nil {
			_ = sessionIDLifecycleWriter.Close()
		}
		sessionIDLifecycleWriter = &lumberjack.Logger{
			Filename:   logPath,
			MaxSize:    5, // MB
			MaxBackups: 3,
			MaxAge:     30, // days
			Compress:   true,
		}
	}

	if _, err := sessionIDLifecycleWriter.Write(line); err != nil {
		return fmt.Errorf("write lifecycle event: %w", err)
	}
	return nil
}
