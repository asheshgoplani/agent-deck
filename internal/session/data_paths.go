package session

import (
	"log/slog"
	"path/filepath"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
)

func dataPath(name string, markers ...string) (string, error) {
	if len(markers) == 0 {
		markers = []string{name}
	}
	return agentpaths.EffectiveDataPath(name, markers...)
}

func runtimeDataPath(name string) (string, error) {
	return dataPath(filepath.Join("runtime", name), "runtime")
}

func logDataPath(name string) (string, error) {
	return dataPath(filepath.Join("logs", name), "logs")
}

// tempAgentDeckPath is the last-resort location for state when the data dir
// cannot be resolved. It lives under a per-user private temp dir
// ("<TempDir>/.agent-deck-<uid>", 0700, owned by us, not a symlink; see
// agentpaths.SecureTempDir) instead of the shared "<TempDir>/.agent-deck"
// another local user could pre-create or symlink.
func tempAgentDeckPath(parts ...string) string {
	base, note := agentpaths.SecureTempDir(".agent-deck")
	if note != "" {
		eventLog.Warn("private_temp_dir_notice", slog.String("dir", base), slog.String("detail", note))
	}
	all := append([]string{base}, parts...)
	return filepath.Join(all...)
}

// TriageDir returns the directory used for watcher triage session results.
func TriageDir() (string, error) {
	return dataPath("triage", "triage")
}
