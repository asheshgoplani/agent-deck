package session

import (
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
)

// HealthLogDir is the profile's health directory, resolved without creating
// profile state. Runtime health samples and the session event journal share it.
func HealthLogDir(profile string) (string, error) {
	profile, err := ResolveProfileForStorage(profile)
	if err != nil {
		return "", err
	}
	dir, err := GetProfileDir(profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs", "health"), nil
}

// SessionEventJournal returns the profile's session event journal, or nil
// (a no-op writer) when [health] session_events = false, health is disabled,
// or the profile cannot be resolved. Callers append and ignore the result:
// journaling never fails the operation it observes.
func SessionEventJournal(profile string) *health.Journal {
	config, err := LoadUserConfig()
	if err != nil || !config.Health.SessionEventsEnabled() {
		return nil
	}
	dir, err := HealthLogDir(profile)
	if err != nil {
		return nil
	}
	return health.NewJournal(dir)
}

// RecordSessionEvent appends one event with the current time. It is the
// one-line hook the CLI paths use (send, restart, stop).
func RecordSessionEvent(profile, sessionID, kind string, detail map[string]any) {
	_ = SessionEventJournal(profile).Append(health.Event{TS: time.Now(), SessionID: sessionID, Kind: kind, Detail: detail})
}
