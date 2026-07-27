package session

import "testing"

// NewStatusFileWatcherForTest returns a StatusFileWatcher backed only by its
// in-memory status map: no fsnotify watcher, no hooks directory under HOME,
// and Start() must not be called on it. It exists so packages outside
// internal/session (notably internal/ui) can drive the hook-status inputs of
// the adaptive-refresh fingerprint (ui.fingerprintSession reads
// GetHookStatus) without a real filesystem watcher.
func NewStatusFileWatcherForTest(t testing.TB) *StatusFileWatcher {
	t.Helper()
	return &StatusFileWatcher{statuses: make(map[string]*HookStatus)}
}

// SetHookStatusForTest installs a hook status exactly as a processed hook file
// would. Production code must never set statuses directly — hook files under
// GetHooksDir() are the only real source.
func (w *StatusFileWatcher) SetHookStatusForTest(t testing.TB, instanceID string, hs *HookStatus) {
	t.Helper()
	w.mu.Lock()
	w.statuses[instanceID] = hs
	w.mu.Unlock()
}

// SetStatusForTest installs a derived SSE status exactly as a live OpenCode
// /event stream would. Production code must never set statuses directly — the
// per-instance stream goroutines are the only real source.
func (w *OpenCodeSSEWatcher) SetStatusForTest(t testing.TB, instanceID string, st *OpenCodeSSEStatus) {
	t.Helper()
	w.mu.Lock()
	w.statuses[instanceID] = st
	w.mu.Unlock()
}
