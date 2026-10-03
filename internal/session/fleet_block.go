package session

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// Fleet-block change detection (issue #2469, design principle 5). The parent's
// UserPromptSubmit hook injects a children snapshot; it used to repeat the
// same text on every prompt. The hook now records a fingerprint of what it
// last injected and skips an unchanged snapshot.

func fleetBlockDir() string {
	return runtimeDirOrTemp("fleet-block")
}

func fleetBlockPath(parentID string) string {
	return filepath.Join(fleetBlockDir(), sanitizeInboxName(parentID)+".fp")
}

// FleetBlockUnchanged reports whether summary equals the last snapshot
// injected for this parent, and records summary as the latest either way.
func FleetBlockUnchanged(parentID, summary string) bool {
	if strings.TrimSpace(parentID) == "" {
		return false
	}
	sum := sha256.Sum256([]byte(summary))
	fp := hex.EncodeToString(sum[:16])
	path := fleetBlockPath(parentID)
	if prev, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(prev)) == fp { // #nosec G304 -- sanitized id under the data dir
		return true
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
		_ = os.WriteFile(path, []byte(fp+"\n"), 0o600)
	}
	return false
}
