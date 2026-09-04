// Package telemetry implements strictly opt-in usage reports.
// Consent, transport and privacy invariants are in docs/TELEMETRY-DESIGN.md.
package telemetry

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/atomicfile"
)

// Consent is the persisted answer to the consent prompt.
type Consent string

const (
	// ConsentUndecided is the default for missing or corrupt state. Sends nothing.
	ConsentUndecided Consent = "undecided"
	// ConsentGranted is the only value that permits a send.
	ConsentGranted Consent = "granted"
	// ConsentDeclined is remembered forever; the prompt is never shown again.
	ConsentDeclined Consent = "declined"
)

// SchemaVersion must change with the payload shape and TELEMETRY.md.
const SchemaVersion = 1

// StateFileName is the state file, stored in the agent-deck data directory.
const StateFileName = "telemetry-state.json"

// DayFormat is the finest granularity in state and payload.
const DayFormat = "2006-01-02"

// State is the on-disk telemetry state.
type State struct {
	Revision        uint64          `json:"revision"`
	ConsentEndpoint string          `json:"consent_endpoint,omitempty"`
	SchemaVersion   int             `json:"schema_version"`
	Consent         Consent         `json:"consent"`
	ConsentVersion  string          `json:"consent_version,omitempty"`
	ConsentDay      string          `json:"consent_day,omitempty"`
	InstallID       string          `json:"install_id,omitempty"`
	Counters        map[string]int  `json:"counters,omitempty"`
	LastAttemptDay  string          `json:"last_attempt_day,omitempty"`
	LastSentDay     string          `json:"last_sent_day,omitempty"`
	LastPayload     json.RawMessage `json:"last_payload,omitempty"`
}

func defaultState() *State {
	return &State{SchemaVersion: SchemaVersion, Consent: ConsentUndecided}
}

// StatePath returns the absolute path of the state file.
func StatePath() (string, error) {
	return agentpaths.EffectiveDataPath(StateFileName, StateFileName)
}

// LoadState reads the state file.
func LoadState() *State {
	path, err := StatePath()
	if err != nil {
		return defaultState()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return defaultState()
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return defaultState()
	}
	switch s.Consent {
	case ConsentGranted, ConsentDeclined:
	default:
		s.Consent = ConsentUndecided
	}
	if s.SchemaVersion != SchemaVersion && s.Consent == ConsentGranted {
		s.Consent = ConsentUndecided
	}
	return &s
}

// SaveState rejects stale revisions and durably replaces state with mode 0600.
func SaveState(s *State) error {
	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()
	if LoadState().Revision != s.Revision {
		return fmt.Errorf("telemetry: state changed; repeat your choice")
	}
	return saveStateLocked(s)
}

// lockState uses a stable sibling file because state is replaced atomically.
// Separate open descriptions serialize goroutines and processes alike.
func lockState() (func(), error) { return lockStateWithFlags(syscall.LOCK_EX) }

func lockStateWithFlags(flags int) (func(), error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), flags); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func saveStateLocked(s *State) error {
	path, err := StatePath()
	if err != nil {
		return fmt.Errorf("telemetry: state path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("telemetry: create dir: %w", err)
	}
	if s.SchemaVersion == 0 {
		s.SchemaVersion = SchemaVersion
	}
	s.Revision++
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("telemetry: marshal state: %w", err)
	}
	if err := atomicfile.WriteFileDurable(path, data, 0600); err != nil {
		return fmt.Errorf("telemetry: write state: %w", err)
	}
	return nil
}

func newInstallID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("telemetry: random install id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

func dayOf(t time.Time) string {
	return t.UTC().Format(DayFormat)
}
