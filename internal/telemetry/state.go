// Package telemetry implements strictly opt-in, anonymous usage events.
// Consent, transport and privacy invariants are in docs/TELEMETRY-DESIGN.md;
// the published field list is TELEMETRY.md (generated from schema.go).
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
	// ConsentUndecided is the default for missing or corrupt state. Records nothing.
	ConsentUndecided Consent = "undecided"
	// ConsentGranted is the only value that permits recording and upload.
	ConsentGranted Consent = "granted"
	// ConsentDeclined is remembered; see ShouldPrompt for the one v1 re-ask.
	ConsentDeclined Consent = "declined"
)

// Level selects how much is recorded.
type Level string

const (
	// LevelFull records every published event.
	LevelFull Level = "full"
	// LevelBasic records app.start, usage.daily and env.snapshot only, without
	// hour_local, weekday_local and ds_session.
	LevelBasic Level = "basic"
)

// SchemaVersion must change with the event schema and TELEMETRY.md. A change
// turns every existing grant back into "undecided" (consent binds to it).
const SchemaVersion = 2

// StateFileName is the state file, stored in the agent-deck data directory.
const StateFileName = "telemetry-state.json"

// DayFormat is the local calendar day format.
const DayFormat = "2006-01-02"

// maxDailyDays bounds the local rollup history.
const maxDailyDays = 15

// State is the on-disk telemetry state. It never leaves the machine; only
// events built from it (TELEMETRY.md) do.
type State struct {
	Revision        uint64          `json:"revision"`
	ConsentEndpoint string          `json:"consent_endpoint,omitempty"`
	SchemaVersion   int             `json:"schema_version"`
	Consent         Consent         `json:"consent"`
	ConsentVersion  string          `json:"consent_version,omitempty"`
	ConsentDay      string          `json:"consent_day,omitempty"`
	InstallID       string          `json:"install_id,omitempty"`
	LastSentDay     string          `json:"last_sent_day,omitempty"`
	LastPayload     json.RawMessage `json:"last_payload,omitempty"`

	// Counters is the v1 daily counter map. It is read only to recognise a
	// v1 install and is dropped on a v2 grant (it was collected under the
	// schema 1 consent).
	Counters map[string]int `json:"counters,omitempty"`

	DeclinedSchema int                     `json:"declined_schema,omitempty"`
	Level          Level                   `json:"level,omitempty"`
	Salt           string                  `json:"salt,omitempty"`
	Seq            int                     `json:"seq,omitempty"`
	FirstSeenDay   string                  `json:"first_seen_day,omitempty"`
	FirstSeenAt    time.Time               `json:"first_seen_at,omitempty"`
	PreV2          bool                    `json:"pre_v2,omitempty"`
	Milestones     uint32                  `json:"milestones,omitempty"`
	Funnel         FunnelState             `json:"funnel,omitempty"`
	Daily          map[string]*DailyRollup `json:"daily,omitempty"`
	Upload         UploadState             `json:"upload,omitempty"`
	TUIOpen        bool                    `json:"tui_open,omitempty"`
	LastVersion    string                  `json:"last_version,omitempty"`

	// prevV1 is the v1 answer found on load ("" when the file was v2 or absent).
	prevV1 Consent
}

// FunnelState holds the local counters behind the funnel milestones.
type FunnelState struct {
	Created           int      `json:"created,omitempty"`
	ToolsUsed         uint32   `json:"tools_used,omitempty"`
	ActivationCreates int      `json:"activation_creates,omitempty"`
	ActivationDays    []string `json:"activation_days,omitempty"`
}

// UploadState tracks the upload schedule and the last result.
type UploadState struct {
	NextTry          time.Time `json:"next_try,omitempty"`
	AttemptsDay      string    `json:"attempts_day,omitempty"`
	AttemptsToday    int       `json:"attempts_today,omitempty"`
	LastAt           time.Time `json:"last_at,omitempty"`
	LastResult       string    `json:"last_result,omitempty"`
	LastErrorKind    string    `json:"last_error_kind,omitempty"`
	LastEvents       int       `json:"last_events,omitempty"`
	RejectedVersion  string    `json:"rejected_version,omitempty"`
	RejectedSinceDay string    `json:"rejected_since_day,omitempty"`
}

func defaultState() *State {
	return &State{SchemaVersion: SchemaVersion, Consent: ConsentUndecided}
}

// StatePath returns the absolute path of the state file.
func StatePath() (string, error) {
	return agentpaths.EffectiveDataPath(StateFileName, StateFileName)
}

// siblingPath returns a file next to the state file.
func siblingPath(name string) (string, error) {
	p, err := StatePath()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(p), name), nil
}

// LoadState reads the state file. Missing or corrupt state is undecided.
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
	if s.SchemaVersion < SchemaVersion {
		s.prevV1 = s.Consent
	}
	if s.SchemaVersion != SchemaVersion && s.Consent == ConsentGranted {
		s.Consent = ConsentUndecided
	}
	if s.Level != LevelBasic {
		s.Level = LevelFull
	}
	return &s
}

// Previous reports the v1 answer this state was migrated from, for the
// telemetry.consent event.
func (s *State) Previous() string {
	switch s.prevV1 {
	case ConsentGranted:
		return "v1_granted"
	case ConsentDeclined:
		return "v1_declined"
	case ConsentUndecided:
		return "v1_undecided"
	}
	return "none"
}

// V1Declined reports a decline recorded by the schema 1 prompt, which counted
// every key (even Enter) as no. It is asked once more (spec open question Q1).
func (s *State) V1Declined() bool {
	return s.Consent == ConsentDeclined && s.DeclinedSchema < SchemaVersion
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
// Separate open descriptions serialize goroutines and processes alike. The
// same lock guards the spool, so recording, upload and disable never interleave.
func lockState() (func(), error) { return lockStateWithFlags(syscall.LOCK_EX) }

func lockStateWithFlags(flags int) (func(), error) {
	path, err := StatePath()
	if err != nil {
		return nil, err
	}
	return flockFile(path+".lock", flags)
}

func flockFile(path string, flags int) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), flags); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

// saveStateLocked durably writes state; used for consent and upload changes.
func saveStateLocked(s *State) error { return writeState(s, true) }

// saveStateFast writes state without fsync; used by per-event recording so
// the UI never waits on the disk. A crash loses at most a few counters.
func saveStateFast(s *State) error { return writeState(s, false) }

func writeState(s *State, durable bool) error {
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
	s.pruneDaily()
	s.Revision++
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("telemetry: marshal state: %w", err)
	}
	write := atomicfile.WriteFile
	if durable {
		write = atomicfile.WriteFileDurable
	}
	if err := write(path, data, 0600); err != nil {
		return fmt.Errorf("telemetry: write state: %w", err)
	}
	return nil
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("telemetry: random: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func newInstallID() (string, error) { return randomHex(16) }

func validInstallID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// dayOf returns the local calendar day of t.
func dayOf(t time.Time) string {
	return t.Local().Format(DayFormat)
}
