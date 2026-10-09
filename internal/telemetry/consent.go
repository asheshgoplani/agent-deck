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

// promptTemplateV3 is the shared CLI/TUI disclosure. {{where}} is the
// destination line (PostHog EU for the default endpoint).
const promptTemplateV3 = `Help improve agent-deck?

Share anonymous usage data with the agent-deck maintainer.

Sent:   tools, features, rounded session counts and lengths,
        active hour and weekday, error types, fleet size,
        version, OS and daily active-install counts.
        Usage ID is resettable; tick ID is random each day.
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
	// PromptKeepsID is shown above the buttons when a yes keeps an existing
	// install id (asked again after an upgrade changed the question).
	PromptKeepsID = "Your anonymous ID is kept; unsent data from before will be sent."
	// PromptKeepsIDNewEndpoint is the same note when the destination changed:
	// data recorded for the old destination is never sent to the new one.
	PromptKeepsIDNewEndpoint = "Your anonymous ID is kept; unsent data for the old endpoint is deleted."
	// PromptTooSmall replaces the question when the terminal is too small.
	PromptTooSmall = "Telemetry is off. Enlarge the window to 78×22 to read the question, or run agent-deck telemetry on in a shell."

	GrantedLine  = "Sharing is on. Nothing is sent before tomorrow. Turn off: agent-deck telemetry off"
	DeclinedLine = "Telemetry stays off. You will not be asked again. Change later: agent-deck telemetry on"
)

// maxWhereWidth keeps the destination line inside the 78-column box.
const maxWhereWidth = 62

// PromptText renders the disclosure for an endpoint.
func PromptText(endpoint string) string {
	return strings.ReplaceAll(promptTemplateV3, "{{where}}", whereLine(endpoint))
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

// ReconsentNote returns the line the prompt adds when a yes would keep an
// existing install id (a grant from an earlier schema, or for another
// endpoint), or "" when a yes creates a new one.
func ReconsentNote(s *State, endpoint string) string {
	switch {
	case s == nil || !hasIdentity(s):
		return ""
	case s.ConsentEndpoint != endpoint:
		return PromptKeepsIDNewEndpoint
	default:
		return PromptKeepsID
	}
}

// hasIdentity reports a usable install id and salt.
func hasIdentity(s *State) bool {
	return validInstallID(s.InstallID) && len(s.Salt) == 64
}

// Grant records consent. An existing install id and salt are always kept:
// upgrades, a schema bump (which only asks again, see LoadState) and a new
// destination never change who the install is. Only reset-id rotates them,
// and only a no or off clears them; a new pair is created when none is
// usable (after a no or off, or a v1 state without a salt). Unsent spool
// lines and rollups recorded before the yes are kept and sent later under
// the same id; they are re-validated against the current schema when read
// and get os, arch and schema when sent. Data is dropped only with a new
// identity or when the destination changed, so events recorded for one
// destination are never sent to another; a kept id keeps its seq counter and
// milestones even then. Nothing records meanwhile: a stale grant does not
// enable recording.
func Grant(s *State, version string, now time.Time) error {
	switch {
	case !hasIdentity(s):
		id, salt, err := newIdentity()
		if err != nil {
			return err
		}
		if err := DeleteSpool(); err != nil {
			return err
		}
		s.resetCollected()
		s.InstallID, s.Salt = id, salt
	case s.ConsentEndpoint != Endpoint():
		if err := DeleteSpool(); err != nil {
			return err
		}
		s.dropUnsent()
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
	s.dropUnsent()
	s.Seq = 0
	s.Milestones = 0
	s.Funnel = FunnelState{}
}

// dropUnsent forgets the rollups waiting to be sent and the upload history
// of one destination. The per-id sequence, milestones and funnel stay, so a
// kept id never repeats a seq number or a one-time milestone.
func (s *State) dropUnsent() {
	s.Counters = nil
	s.LastPayload = nil
	s.LastSentDay = ""
	s.Daily = nil
	s.Upload = UploadState{}
	s.TUIOpen = false
	s.OpenHour = nil
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
	s.OpenHour = nil
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
		if !hasIdentity(s) {
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
// decision; callers must confirm it interactively first. Unless the level
// stays full, the stored open hour is forgotten, so an hour sampled at one
// level never ships at another.
func SetLevel(l Level) (*State, error) {
	unlock, err := lockState()
	if err != nil {
		return nil, err
	}
	defer unlock()
	s := LoadState()
	if !s.keepsOpenHour() || l != LevelFull {
		s.OpenHour = nil
	}
	s.Level = l
	return s, saveStateLocked(s)
}
