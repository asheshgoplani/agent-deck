package telemetry

import (
	"errors"
	"strings"
	"time"
)

// DocsURL is the user documentation linked from the consent prompt.
const DocsURL = "https://github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md"

// PromptWidth and PromptHeight are the smallest terminal that shows the
// whole question; below it the TUI refuses to accept.
const (
	PromptWidth  = 78
	PromptHeight = 22
)

// promptTemplateV2 is the shared CLI/TUI disclosure. {{where}} is the
// destination line (PostHog EU for the default endpoint).
const promptTemplateV2 = `Help improve agent-deck?

Share anonymous usage data with the agent-deck maintainer.

Sent:   tools and features you use, session counts and lengths,
        the hour and weekday you are active, error types, fleet
        size, version and OS. Numbers are rounded into ranges.
        A random ID links your reports; reset it any time.
Never:  prompts, output, titles, paths, repo, host or user names,
        or anything you type. IP addresses are discarded.
Where:  {{where}}
Check:  agent-deck telemetry preview   (exactly what would be sent)
Off:    agent-deck telemetry off   or   DO_NOT_TRACK=1
More:   github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md`

// Button labels and the key legend of the TUI prompt.
const (
	PromptAccept = "Share anonymous data"
	PromptNo     = "No thanks"
	PromptLegend = "Enter: confirm highlighted · n / Esc: no · Ctrl-C: ask me later"
	// PromptV1Declined is shown above the buttons for installs that declined
	// the schema 1 prompt, which counted every key as no.
	PromptV1Declined = "You said no to an earlier, smaller version of this question."
	// PromptTooSmall replaces the question when the terminal is too small.
	PromptTooSmall = "Telemetry is off. Enlarge the window to 78×22 to read the question, or run agent-deck telemetry on in a shell."

	GrantedLine  = "Sharing is on. Nothing is sent before tomorrow. Turn off: agent-deck telemetry off"
	DeclinedLine = "Telemetry stays off. You will not be asked again. Change later: agent-deck telemetry on"
)

// maxWhereWidth keeps the destination line inside the 78-column box.
const maxWhereWidth = 62

// PromptText renders the disclosure for an endpoint.
func PromptText(endpoint string) string {
	return strings.ReplaceAll(promptTemplateV2, "{{where}}", whereLine(endpoint))
}

func whereLine(endpoint string) string {
	if strings.TrimRight(endpoint, "/") == DefaultEndpoint {
		return "a few times a day to PostHog (EU). Kept for 1 year."
	}
	return "a few times a day to " + endpoint
}

// PromptFits reports whether the destination line fits the fixed-width box.
func PromptFits(endpoint string) bool {
	return len(whereLine(endpoint)) <= maxWhereWidth
}

// ShouldPrompt reports whether the one-time consent prompt may be shown now:
// undecided, or declined once under schema 1; interactive; no hard off; not
// in log mode (which never grants).
func ShouldPrompt(s *State) bool {
	if s == nil || !(s.Consent == ConsentUndecided || s.V1Declined()) {
		return false
	}
	if HardDisabled() || LogMode() || ValidateEndpoint(Endpoint()) != nil {
		return false
	}
	return Interactive()
}

// Grant records consent. A new install id and salt are created unless the
// existing ones were granted for this exact endpoint and schema; a new
// identity also deletes the spool, so events recorded under an earlier
// consent or destination can never be sent under this one. Nothing records
// into the spool meanwhile: the stale grant does not enable recording.
func Grant(s *State, version string, now time.Time) error {
	if !validInstallID(s.InstallID) || len(s.Salt) != 64 || s.ConsentEndpoint != Endpoint() || s.SchemaVersion != SchemaVersion {
		id, salt, err := newIdentity()
		if err != nil {
			return err
		}
		if err := DeleteSpool(); err != nil {
			return err
		}
		s.resetCollected()
		s.InstallID, s.Salt = id, salt
	}
	s.SchemaVersion = SchemaVersion
	s.ConsentEndpoint = Endpoint()
	s.Consent = ConsentGranted
	s.ConsentVersion = version
	s.ConsentDay = dayOf(now)
	if s.Level == "" {
		s.Level = LevelFull
	}
	s.initFirstSeen(now)
	return nil
}

// newIdentity returns a fresh random install id and HMAC salt.
func newIdentity() (id, salt string, err error) {
	if id, err = newInstallID(); err != nil {
		return "", "", err
	}
	if salt, err = randomHex(32); err != nil {
		return "", "", err
	}
	return id, salt, nil
}

// resetCollected forgets everything recorded under an install id.
func (s *State) resetCollected() {
	s.Counters = nil
	s.LastPayload = nil
	s.LastSentDay = ""
	s.Seq = 0
	s.Daily = nil
	s.Upload = UploadState{}
	s.Milestones = 0
	s.Funnel = FunnelState{}
	s.TUIOpen = false
}

// Decline records a refusal and forgets the id, salt and everything recorded.
// The local first-seen facts stay (they never left the machine).
func Decline(s *State, version string, now time.Time) {
	s.SchemaVersion = SchemaVersion
	s.DeclinedSchema = SchemaVersion
	s.Consent = ConsentDeclined
	s.ConsentVersion = version
	s.ConsentDay = dayOf(now)
	s.InstallID = ""
	s.Salt = ""
	s.ConsentEndpoint = ""
	s.resetCollected()
}

// RotateInstallID replaces the install id and salt, and forgets the spool
// and rollups recorded under the old id. Callers delete the spool.
func RotateInstallID(s *State) error {
	id, salt, err := newIdentity()
	if err != nil {
		return err
	}
	s.InstallID, s.Salt = id, salt
	s.Seq = 0
	s.Daily = nil
	s.LastPayload = nil
	s.LastSentDay = ""
	return nil
}

// ResetID rotates the install id and deletes the spool, under the lock.
func ResetID() (*State, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	s := LoadState()
	if s.Consent != ConsentGranted {
		return s, errors.New("telemetry: no install id exists because telemetry is not enabled")
	}
	if err := RotateInstallID(s); err != nil {
		return nil, err
	}
	if err := DeleteSpool(); err != nil {
		return nil, err
	}
	return s, saveStateLocked(s)
}

// Enabled reports whether recording is permitted by consent and the
// hard-disable switches, ignoring interactivity.
func Enabled(s *State) (bool, DisableReason) {
	if r := HardDisableReason(); r != ReasonNone {
		return false, r
	}
	switch s.Consent {
	case ConsentGranted:
		if s.SchemaVersion != SchemaVersion || s.ConsentEndpoint != Endpoint() {
			return false, DisableReason("endpoint or schema changed; interactive consent required")
		}
		if !validInstallID(s.InstallID) || len(s.Salt) != 64 {
			return false, DisableReason("invalid install id; interactive consent required")
		}
		return true, ReasonNone
	case ConsentDeclined:
		return false, ReasonDeclined
	default:
		return false, ReasonUndecided
	}
}

// Disable commits a fresh refusal and deletes the spool under the lock. It
// may wait for an in-flight upload (at most uploadDeadline); once it returns,
// nothing further is sent and the spool is gone.
func Disable(version string, now time.Time) error {
	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()
	s := LoadState()
	Decline(s, version, now)
	if err := DeleteSpool(); err != nil {
		return err
	}
	return saveStateLocked(s)
}

// SetLevel stores the recording level. Raising basic to full is a consent
// decision; callers must confirm it interactively first.
func SetLevel(l Level) (*State, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	s := LoadState()
	s.Level = l
	return s, saveStateLocked(s)
}
