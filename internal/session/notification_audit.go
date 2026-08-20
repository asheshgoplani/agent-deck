package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/desktopnotify"
)

// Notification audit log.
//
// Every channel that can interrupt the human — the GUI helper banners, the
// desknotify fallback banners, and the Claude hook banners in
// ~/.claude/hooks/lib/agentdeck-banner.sh — appends one JSON object per
// decision to the SAME file. Without it there is no way to answer the only
// question that matters when someone says "I get too many notifications":
// which channel produced them, for which sessions, and which of those the
// person was already looking at.
//
// The daemon's existing desktop_notification_sent breadcrumb cannot serve this
// purpose: it is DEBUG (the daemon logs at INFO), it only covers one of the two
// desktop channels, it records deliveries but never suppressions, and
// debug.log's 10MB×5 rotation ages out within hours under normal TUI volume.
//
// Writes are best-effort and must never affect delivery: a failed audit is
// silently dropped.

const (
	// notificationAuditEnv overrides the audit path (tests, and the shell
	// hooks share the same variable name).
	notificationAuditEnv = "AD_NOTIFY_AUDIT"
	// notificationAuditMaxBytes caps the live file. At a realistic few
	// hundred records/day (~300 bytes each) this is months of history; the
	// cap only guards against a pathological notification storm.
	notificationAuditMaxBytes = 16 << 20
)

var notificationAuditMu sync.Mutex

// NotificationAuditRecord is one notification decision. Field names match the
// shell hooks' records so a single reader can process the whole stream.
type NotificationAuditRecord struct {
	TS         string  `json:"ts"`
	Epoch      float64 `json:"epoch"`
	Src        string  `json:"src"`
	Kind       string  `json:"kind,omitempty"`
	Class      string  `json:"class,omitempty"`
	Decision   string  `json:"decision"`
	Reason     string  `json:"reason,omitempty"`
	IID        string  `json:"iid,omitempty"`
	Name       string  `json:"name,omitempty"`
	Profile    string  `json:"profile,omitempty"`
	Project    string  `json:"project,omitempty"`
	Parent     string  `json:"parent,omitempty"`
	ToStatus   string  `json:"to_status,omitempty"`
	DoneStatus string  `json:"done_status,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// NotificationAuditPath reports the shared audit log location.
func NotificationAuditPath() (string, error) {
	if override := strings.TrimSpace(os.Getenv(notificationAuditEnv)); override != "" {
		return override, nil
	}
	return agentpaths.CachePath("notification-audit.jsonl")
}

// auditNotification appends one record. Best-effort: every failure is silent
// so notification delivery never depends on the audit trail.
func auditNotification(rec NotificationAuditRecord) {
	path, err := NotificationAuditPath()
	if err != nil {
		return
	}
	now := time.Now()
	rec.TS = now.UTC().Format(time.RFC3339)
	rec.Epoch = float64(now.UnixNano()) / 1e9
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}

	notificationAuditMu.Lock()
	defer notificationAuditMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Keep exactly one generation of history: a rename is atomic, so a
	// concurrent reader never observes a truncated file.
	if info, err := os.Stat(path); err == nil && info.Size() > notificationAuditMaxBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// auditDesktopEvent records a GUI-helper notification decision, deriving the
// class the same way the transport does so the audit shows what the human
// would actually have seen.
func auditDesktopEvent(src string, inst *Instance, event desktopnotify.SourceEvent, decision, reason string, err error) {
	rec := NotificationAuditRecord{
		Src:        src,
		Kind:       strings.TrimSpace(event.Kind),
		Decision:   decision,
		Reason:     reason,
		IID:        strings.TrimSpace(event.SessionID),
		Name:       strings.TrimSpace(event.Title),
		Profile:    strings.TrimSpace(event.Profile),
		Project:    strings.TrimSpace(event.Project),
		ToStatus:   strings.TrimSpace(event.ToStatus),
		DoneStatus: strings.TrimSpace(event.DoneStatus),
	}
	if inst != nil {
		rec.Parent = strings.TrimSpace(inst.ParentSessionID)
	}
	if normalized, ok := desktopnotify.Normalize(event); ok {
		rec.Class = string(normalized.Class)
	} else {
		rec.Class = "unclassified"
	}
	if err != nil {
		rec.Error = err.Error()
	}
	auditNotification(rec)
}
