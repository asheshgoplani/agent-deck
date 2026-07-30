package session

import (
	"fmt"
	"time"
)

const HookStateSchemaV1 = "agent-deck.hook-state/v1"

// HookStateDocument extends the latest hook event with retained, monotonic
// turn generations. The legacy fields keep their existing JSON names so older
// Agent Deck versions can continue reading files written by this version.
type HookStateDocument struct {
	SchemaVersion string `json:"hook_state_schema_version,omitempty"`

	Status    string `json:"status"`
	SessionID string `json:"session_id,omitempty"`
	// StateSessionID binds retained generations to one upstream session while
	// SessionID preserves the legacy latest-event field, which may be empty.
	StateSessionID string `json:"hook_state_session_id,omitempty"`
	Event          string `json:"event"`
	Timestamp      int64  `json:"ts"`

	DoneStatus     string `json:"done_status,omitempty"`
	DoneSummary    string `json:"done_summary,omitempty"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	Cwd            string `json:"cwd,omitempty"`

	Generation                  uint64 `json:"generation,omitempty"`
	LastTurnStartedGeneration   uint64 `json:"last_turn_started_generation,omitempty"`
	LastTurnCompletedGeneration uint64 `json:"last_turn_completed_generation,omitempty"`
	LastTurnStartedAt           int64  `json:"last_turn_started_at,omitempty"`
	LastTurnCompletedAt         int64  `json:"last_turn_completed_at,omitempty"`
}

type HookStateEventKind string

const (
	HookStatusOnly    HookStateEventKind = "status"
	HookTurnStarted   HookStateEventKind = "turn_started"
	HookTurnCompleted HookStateEventKind = "turn_completed"
)

type HookStateEvent struct {
	Kind           HookStateEventKind
	Status         string
	SessionID      string
	Event          string
	At             time.Time
	DoneStatus     string
	DoneSummary    string
	TranscriptPath string
	Cwd            string
}

// AdvanceHookState retains turn ordering even when start and completion share
// one Unix second. A changed upstream session cannot inherit an old completion.
func AdvanceHookState(previous HookStateDocument, event HookStateEvent) (HookStateDocument, error) {
	switch event.Kind {
	case HookStatusOnly, HookTurnStarted, HookTurnCompleted:
	default:
		return HookStateDocument{}, fmt.Errorf("unknown hook state event kind %q", event.Kind)
	}
	if event.Status == "" {
		return HookStateDocument{}, fmt.Errorf("hook status is required")
	}

	next := previous
	if next.SchemaVersion == "" {
		next.SchemaVersion = HookStateSchemaV1
	}
	previousStateSessionID := previous.StateSessionID
	if previousStateSessionID == "" {
		previousStateSessionID = previous.SessionID
	}
	if previousStateSessionID != "" && event.SessionID != "" && previousStateSessionID != event.SessionID {
		next.LastTurnStartedGeneration = 0
		next.LastTurnCompletedGeneration = 0
		next.LastTurnStartedAt = 0
		next.LastTurnCompletedAt = 0
	}

	// Preserve the legacy latest-event shape: an event without a session ID
	// keeps session_id empty. The retained identity remains available separately.
	next.SessionID = event.SessionID
	if event.SessionID != "" {
		next.StateSessionID = event.SessionID
	}
	if event.Status == "dead" && event.SessionID == "" {
		next.StateSessionID = ""
		next.LastTurnStartedGeneration = 0
		next.LastTurnCompletedGeneration = 0
		next.LastTurnStartedAt = 0
		next.LastTurnCompletedAt = 0
	}

	at := event.At
	if at.IsZero() {
		at = time.Now()
	}
	next.Generation++
	next.Status = event.Status
	next.Event = event.Event
	next.Timestamp = at.Unix()
	next.DoneStatus = event.DoneStatus
	next.DoneSummary = event.DoneSummary
	next.TranscriptPath = event.TranscriptPath
	next.Cwd = event.Cwd

	switch event.Kind {
	case HookTurnStarted:
		next.LastTurnStartedGeneration = next.Generation
		next.LastTurnStartedAt = at.UnixNano()
	case HookTurnCompleted:
		next.LastTurnCompletedGeneration = next.Generation
		next.LastTurnCompletedAt = at.UnixNano()
	}
	return next, nil
}

func validateHookInstanceID(instanceID string) error {
	if len(instanceID) == 0 || len(instanceID) > 128 || instanceID == "." || instanceID == ".." {
		return fmt.Errorf("invalid hook instance ID")
	}
	for _, r := range instanceID {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '-', r == '_':
		default:
			return fmt.Errorf("invalid hook instance ID")
		}
	}
	return nil
}
