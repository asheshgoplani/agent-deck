package session

import (
	"os"
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

func tempAgentDeckPath(parts ...string) string {
	all := append([]string{os.TempDir(), ".agent-deck"}, parts...)
	return filepath.Join(all...)
}

// TriageDir returns the directory used for watcher triage session results.
func TriageDir() (string, error) {
	return dataPath("triage", "triage")
}
