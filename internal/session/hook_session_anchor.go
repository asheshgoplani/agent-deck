package session

import (
	"os"
	"path/filepath"
	"strings"
)

// Hook session anchor sidecar:
// Hook payloads can have empty session_id for some events, which may otherwise
// lose restart-critical session binding. We persist the last non-empty ID in a
// .sid sidecar file and only use it as a read-time fallback. Hook JSON semantics
// remain unchanged for backward compatibility.

// HookSessionAnchorPath returns the sidecar file path used to persist the
// last known non-empty hook session ID for one instance.
func HookSessionAnchorPath(instanceID string) string {
	return filepath.Join(GetHooksDir(), instanceID+".sid")
}

// ReadHookSessionAnchor reads the persisted hook session ID sidecar.
func ReadHookSessionAnchor(instanceID string) string {
	if strings.TrimSpace(instanceID) == "" {
		return ""
	}
	data, err := os.ReadFile(HookSessionAnchorPath(instanceID))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// WriteHookSessionAnchor persists the latest non-empty hook session ID.
func WriteHookSessionAnchor(instanceID, sessionID string) {
	instanceID = strings.TrimSpace(instanceID)
	sessionID = strings.TrimSpace(sessionID)
	if instanceID == "" || sessionID == "" {
		return
	}
	hooksDir := GetHooksDir()
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		return
	}
	path := HookSessionAnchorPath(instanceID)
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, []byte(sessionID), 0644); err != nil {
		return
	}
	_ = os.Rename(tmpPath, path)
}

// ClearHookSessionAnchor removes the persisted hook session ID sidecar.
func ClearHookSessionAnchor(instanceID string) {
	if strings.TrimSpace(instanceID) == "" {
		return
	}
	_ = os.Remove(HookSessionAnchorPath(instanceID))
}
