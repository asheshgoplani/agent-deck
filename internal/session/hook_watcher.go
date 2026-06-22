package session

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

var hookLog = logging.ForComponent(logging.CompSession)

const maxHookStatusFileSize = 64 << 10 // status files are a few hundred bytes

// readStatusFileNoFollow reads a hook status file without following a
// final-component symlink (O_NOFOLLOW) and bounded in size, so a compromised
// sandbox cannot symlink <id>.json at a sibling/host/device file to exfiltrate
// or DoS the shared notify-daemon, nor OOM it with a huge real <id>.json.
func readStatusFileNoFollow(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, maxHookStatusFileSize))
}

// HookStatus holds the decoded status from a hook status file.
type HookStatus struct {
	Status    string    // running, idle, waiting, dead
	SessionID string    // Claude session ID
	Event     string    // Hook event name
	UpdatedAt time.Time // When this status was received
	// DoneStatus/DoneSummary carry a worker-printed completion sentinel
	// detected on the Stop edge (issue #1186). Empty for ordinary turns.
	DoneStatus  string // "ok" or "fail" when a completion sentinel was seen
	DoneSummary string // free-text completion summary
	// TranscriptPath is set when the Stop-edge sentinel scan was inconclusive
	// because the turn's assistant record had not flushed yet (issue #1186
	// flush race). The transition daemon re-scans this path on its poll loop.
	TranscriptPath string
}

// StatusFileWatcher watches ~/.agent-deck/hooks/ for status file changes
// and updates instance hook status in real time.
type StatusFileWatcher struct {
	hooksDir string
	// sandboxDir is hooksDir/sandbox — the parent of the per-instance scoped
	// subdirs that sandboxed sessions bridge from their containers. Each
	// sandbox session writes …/hooks/sandbox/<id>/<id>.json; the watcher
	// observes these by watching this dir for subdir-create events and adding
	// a watch on each per-instance subdir.
	sandboxDir string
	watcher    *fsnotify.Watcher

	mu       sync.RWMutex
	statuses map[string]*HookStatus // instance_id -> latest hook status

	ctx    context.Context
	cancel context.CancelFunc

	// onChange is called when a hook status changes (for TUI refresh)
	onChange func()
}

// NewStatusFileWatcher creates a new watcher for the hooks directory.
// Call Start() to begin watching.
func NewStatusFileWatcher(onChange func()) (*StatusFileWatcher, error) {
	hooksDir := GetHooksDir()

	// Ensure directory exists
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return nil, err
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &StatusFileWatcher{
		hooksDir:   hooksDir,
		sandboxDir: filepath.Join(hooksDir, "sandbox"),
		watcher:    watcher,
		statuses:   make(map[string]*HookStatus),
		ctx:        ctx,
		cancel:     cancel,
		onChange:   onChange,
	}, nil
}

// Start begins watching the hooks directory. Must be called in a goroutine.
func (w *StatusFileWatcher) Start() {
	if err := w.watcher.Add(w.hooksDir); err != nil {
		hookLog.Warn("hook_watcher_add_failed", slog.String("dir", w.hooksDir), slog.String("error", err.Error()))
		return
	}

	// Also watch the per-instance sandbox subtree so sandboxed sessions' scoped
	// status writes are observed live. We watch the parent (sandboxDir) for
	// subdir-create events and add a watch on each per-instance subdir (fsnotify
	// is non-recursive, so each dir must be registered explicitly). A failure
	// here is non-fatal: flat-dir (non-sandbox) sessions keep working.
	if err := os.MkdirAll(w.sandboxDir, 0o700); err != nil {
		hookLog.Warn("hook_watcher_sandbox_mkdir_failed", slog.String("dir", w.sandboxDir), slog.String("error", err.Error()))
	} else if err := w.watcher.Add(w.sandboxDir); err != nil {
		hookLog.Warn("hook_watcher_add_failed", slog.String("dir", w.sandboxDir), slog.String("error", err.Error()))
	} else {
		w.addExistingScopedWatches()
	}

	// Load any existing status files on startup (flat + scoped)
	w.loadExisting()

	// Debounce timer: coalesce rapid file events
	var debounceTimer *time.Timer
	pendingFiles := make(map[string]bool)
	var pendingMu sync.Mutex

	for {
		select {
		case <-w.ctx.Done():
			return

		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}

			// A new per-instance sandbox subdir appeared — register a watch on
			// it so its status-file writes are observed (fsnotify only reports
			// events for explicitly-added dirs). Then scan it once in case the
			// status file was written before the watch was added.
			if event.Op&fsnotify.Create != 0 && filepath.Dir(event.Name) == w.sandboxDir {
				if info, statErr := os.Stat(event.Name); statErr == nil && info.IsDir() {
					if addErr := w.watcher.Add(event.Name); addErr != nil {
						hookLog.Warn("hook_watcher_add_scoped_failed", slog.String("dir", event.Name), slog.String("error", addErr.Error()))
					} else {
						w.loadDir(event.Name)
					}
					continue
				}
			}

			// Only process .json file writes/creates
			if filepath.Ext(event.Name) != ".json" {
				continue
			}
			if event.Op&(fsnotify.Create|fsnotify.Write) == 0 {
				continue
			}

			pendingMu.Lock()
			pendingFiles[event.Name] = true
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(100*time.Millisecond, func() {
				pendingMu.Lock()
				files := make([]string, 0, len(pendingFiles))
				for f := range pendingFiles {
					files = append(files, f)
				}
				pendingFiles = make(map[string]bool)
				pendingMu.Unlock()

				for _, f := range files {
					w.processFile(f)
				}
			})
			pendingMu.Unlock()

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			if isOverflowError(err) {
				w.handleOverflow(err)
				continue
			}
			hookLog.Warn("hook_watcher_error", slog.String("error", err.Error()))
		}
	}
}

// isOverflowError reports whether err is (or wraps) fsnotify.ErrEventOverflow.
func isOverflowError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, fsnotify.ErrEventOverflow)
}

// handleOverflow recovers from an inotify queue overflow by re-walking the
// hooks directory from disk and atomically replacing the in-memory status
// map. After overflow, individual file events were dropped, so the in-memory
// map can be arbitrarily out of sync with disk; a full re-scan is the only
// reliable recovery.
func (w *StatusFileWatcher) handleOverflow(err error) {
	hookLog.Warn("hook_watcher_overflow_resync",
		slog.String("dir", w.hooksDir),
		slog.String("error", errString(err)),
	)

	rebuilt := w.scanDisk()

	w.mu.Lock()
	w.statuses = rebuilt
	w.mu.Unlock()

	if w.onChange != nil {
		w.onChange()
	}
}

// scanDisk reads every .json hook status file in hooksDir and returns a
// fresh map. Errors on individual files are skipped (they're either
// mid-write or corrupt; the next event will retry).
func (w *StatusFileWatcher) scanDisk() map[string]*HookStatus {
	out := make(map[string]*HookStatus)
	// Flat top-level files (non-sandbox sessions). A read error here is logged
	// because the flat dir is expected to exist.
	if entries, err := os.ReadDir(w.hooksDir); err != nil {
		hookLog.Warn("hook_watcher_scan_read_dir_failed",
			slog.String("dir", w.hooksDir),
			slog.String("error", err.Error()),
		)
	} else {
		w.scanDirEntriesInto(out, w.hooksDir, entries)
	}
	// Per-instance scoped subdirs (sandbox sessions). Missing/absent is benign.
	if subdirs, err := os.ReadDir(w.sandboxDir); err == nil {
		for _, sub := range subdirs {
			if !sub.IsDir() {
				continue
			}
			dir := filepath.Join(w.sandboxDir, sub.Name())
			if entries, rerr := os.ReadDir(dir); rerr == nil {
				w.scanDirEntriesInto(out, dir, entries)
			}
		}
	}
	return out
}

// instanceIDForStatusFile derives the instance ID that OWNS the status file at
// filePath, binding identity to the file's LOCATION — not just its name:
//
//   - Flat top-level file …/hooks/<id>.json (non-sandbox sessions): keyed by
//     filename, as before.
//   - Per-instance scoped file …/hooks/sandbox/<id>/<id>.json (sandbox
//     sessions): keyed by the OWNING SUBDIR name, and accepted ONLY when the
//     basename equals "<subdir>.json".
//
// A sandboxed container can write anywhere inside its OWN …/hooks/sandbox/<id>/
// subdir, but it controls the filename — including naming a file after a victim
// session. Keying purely by filename (the pre-fix behaviour) let such a file
// land at statuses["<victim>"], forging the victim's terminal transition and
// injecting an attacker-chosen done_summary into the conductor's turn. Binding
// the ID to the owning subdir and rejecting any non-matching basename closes
// the forge-sibling and inject-into-conductor vectors. ok=false means the file
// is foreign (or sits in an unrecognized location) and must NOT be stored.
func (w *StatusFileWatcher) instanceIDForStatusFile(filePath string) (string, bool) {
	if filepath.Ext(filePath) != ".json" {
		return "", false
	}
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)
	// Per-instance scoped subdir: …/hooks/sandbox/<id>/…  (parent-of-dir is the
	// sandbox root, so dir itself is the per-instance subdir named <id>).
	if w.sandboxDir != "" && filepath.Dir(dir) == w.sandboxDir {
		owner := filepath.Base(dir)
		if base != owner+".json" {
			return "", false // foreign-named file inside someone else's subdir
		}
		return owner, true
	}
	// Flat top-level file: …/hooks/<id>.json (non-sandbox sessions).
	if dir == w.hooksDir {
		return strings.TrimSuffix(base, ".json"), true
	}
	// A file directly under …/hooks/sandbox/ (not in a per-instance subdir), or
	// any deeper/unexpected location, is not an authoritative status file.
	return "", false
}

// scanDirEntriesInto parses every .json status file in dir and writes the
// decoded HookStatus into out keyed by its scope-bound instance ID. Files whose
// name does not match their owning sandbox subdir are foreign and skipped.
func (w *StatusFileWatcher) scanDirEntriesInto(out map[string]*HookStatus, dir string, entries []os.DirEntry) {
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		instanceID, ok := w.instanceIDForStatusFile(path)
		if !ok {
			continue // foreign-named file in a scoped subdir, or unexpected path
		}
		// No-follow + size-bounded: a compromised sandbox could symlink its
		// <id>.json at a sibling/host/device file, or write a huge real file, to
		// exfiltrate or DoS the daemon. O_NOFOLLOW errors out on a symlink; the
		// bound caps the read.
		data, rerr := readStatusFileNoFollow(path)
		if rerr != nil {
			continue
		}
		var raw struct {
			Status         string `json:"status"`
			SessionID      string `json:"session_id"`
			Event          string `json:"event"`
			Timestamp      int64  `json:"ts"`
			DoneStatus     string `json:"done_status"`
			DoneSummary    string `json:"done_summary"`
			TranscriptPath string `json:"transcript_path"`
		}
		if uerr := json.Unmarshal(data, &raw); uerr != nil {
			continue
		}
		out[instanceID] = &HookStatus{
			Status:         raw.Status,
			SessionID:      raw.SessionID,
			Event:          raw.Event,
			UpdatedAt:      time.Unix(raw.Timestamp, 0),
			DoneStatus:     raw.DoneStatus,
			DoneSummary:    raw.DoneSummary,
			TranscriptPath: raw.TranscriptPath,
		}
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Stop shuts down the watcher.
func (w *StatusFileWatcher) Stop() {
	w.cancel()
	_ = w.watcher.Close()
}

// GetHookStatus returns the hook status for an instance, or nil if not available.
func (w *StatusFileWatcher) GetHookStatus(instanceID string) *HookStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.statuses[instanceID]
}

// ClearHookStatus removes the cached hook status for an instance.
func (w *StatusFileWatcher) ClearHookStatus(instanceID string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.statuses, instanceID)
}

// loadExisting reads all current status files on startup — both the flat
// top-level files (non-sandbox sessions) and the per-instance scoped subdir
// files (sandbox sessions under …/hooks/sandbox/<id>/).
func (w *StatusFileWatcher) loadExisting() {
	w.loadDir(w.hooksDir)
	w.loadScopedDirs()
}

// loadDir processes every .json status file directly inside dir (non-recursive).
func (w *StatusFileWatcher) loadDir(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		w.processFile(filepath.Join(dir, entry.Name()))
	}
}

// loadScopedDirs processes the status file inside each per-instance sandbox
// subdir under sandboxDir.
func (w *StatusFileWatcher) loadScopedDirs() {
	entries, err := os.ReadDir(w.sandboxDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		w.loadDir(filepath.Join(w.sandboxDir, entry.Name()))
	}
}

// addExistingScopedWatches registers an fsnotify watch on each per-instance
// sandbox subdir that already exists when the watcher starts.
func (w *StatusFileWatcher) addExistingScopedWatches() {
	entries, err := os.ReadDir(w.sandboxDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(w.sandboxDir, entry.Name())
		if err := w.watcher.Add(dir); err != nil {
			hookLog.Warn("hook_watcher_add_scoped_failed", slog.String("dir", dir), slog.String("error", err.Error()))
		}
	}
}

// processFile reads a status file and updates the internal map.
// Closes logging-review G9/G10/G11: corrupt files now WARN instead of
// fail-open silently; success-path logs at INFO with file path so a hook
// audit can be done without opening SQLite.
func (w *StatusFileWatcher) processFile(filePath string) {
	// Bind identity to the file's LOCATION before reading it. A scoped sandbox
	// file is authoritative ONLY for the instance that owns its subdir; a
	// foreign-named file planted inside another instance's writable subdir is
	// rejected here so a compromised container cannot forge a sibling's status
	// (or inject a done_summary into the conductor via a victim-named file).
	instanceID, ok := w.instanceIDForStatusFile(filePath)
	if !ok {
		hookLog.Warn("hook_file_foreign_scope_rejected", slog.String("path", filePath))
		return
	}

	// No-follow + size-bounded read (see readStatusFileNoFollow): O_NOFOLLOW
	// makes a symlinked <id>.json error out here rather than disclose the
	// symlink target, and the bound caps a huge real file so a compromised
	// sandbox cannot OOM the shared notify-daemon.
	data, err := readStatusFileNoFollow(filePath)
	if err != nil {
		// Not-exist is benign (file was deleted between event and read).
		// Anything else (incl. ELOOP on a symlinked path) is real corruption.
		if !os.IsNotExist(err) {
			hookLog.Warn("hook_file_corrupt",
				slog.String("path", filePath),
				slog.String("reason", "read"),
				slog.String("error", err.Error()),
			)
		}
		return
	}

	var status struct {
		Status         string `json:"status"`
		SessionID      string `json:"session_id"`
		Event          string `json:"event"`
		Timestamp      int64  `json:"ts"`
		DoneStatus     string `json:"done_status"`
		DoneSummary    string `json:"done_summary"`
		TranscriptPath string `json:"transcript_path"`
	}
	if err := json.Unmarshal(data, &status); err != nil {
		hookLog.Warn("hook_file_corrupt",
			slog.String("path", filePath),
			slog.String("reason", "unmarshal"),
			slog.String("error", err.Error()),
			slog.Int("bytes_read", len(data)),
		)
		return
	}

	hookStatus := &HookStatus{
		Status:         status.Status,
		SessionID:      status.SessionID,
		Event:          status.Event,
		UpdatedAt:      time.Unix(status.Timestamp, 0),
		DoneStatus:     status.DoneStatus,
		DoneSummary:    status.DoneSummary,
		TranscriptPath: status.TranscriptPath,
	}

	w.mu.Lock()
	w.statuses[instanceID] = hookStatus
	w.mu.Unlock()

	hookLog.Info("hook_status_updated",
		slog.String("instance", instanceID),
		slog.String("status", status.Status),
		slog.String("event", status.Event),
		slog.String("path", filePath),
	)

	if w.onChange != nil {
		w.onChange()
	}
}

// GetHooksDir returns the path to the hooks status directory.
func GetHooksDir() string {
	path, err := dataPath("hooks", "hooks")
	if err != nil {
		return tempAgentDeckPath("hooks")
	}
	return path
}
