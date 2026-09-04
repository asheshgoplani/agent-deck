package telemetry

import (
	"strings"
	"time"
)

// DocsURL is the user documentation linked from the consent prompt.
const DocsURL = "https://github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md"

// promptTemplate is the exact consent copy shared by the TUI dialog and
// `agent-deck telemetry enable`. {{endpoint}} is substituted with the
// effective endpoint so the person sees the real destination.
const promptTemplate = `Help improve agent-deck? (optional, off by default)

agent-deck can send one small anonymous usage report per day so the
maintainer can see which features are used. Nothing is sent unless you
say yes here. A random id links reports until you reset it.

What is sent:      a random install id, agent-deck version, OS and CPU
                   type, and counts of features used (for example how
                   many sessions were started per tool, whether remote
                   or conductor commands were used, TUI vs CLI).
What is never sent: session titles, prompts, file paths, commands,
                   hostnames, usernames, or timestamps finer than the day.
                   Receiver operators must disable IP/access logging.
Where:             one HTTPS POST, at most once per day, to
                   {{endpoint}}
Turn off any time: agent-deck telemetry disable
                   or set AGENTDECK_TELEMETRY=0 / DO_NOT_TRACK=1
Preview the current payload: agent-deck telemetry preview
Last acknowledged payload: agent-deck telemetry show-last
Details: ` + DocsURL

// PromptChoices is the one-line key legend under the prompt.
const PromptChoices = "[y] Yes, send anonymous usage reports    [n] No (remembered, you will not be asked again)"

// PromptText renders the consent prompt for the given endpoint.
func PromptText(endpoint string) string {
	return strings.ReplaceAll(promptTemplate, "{{endpoint}}", endpoint)
}

// ShouldPrompt reports whether the one-time consent prompt may be shown right now.
func ShouldPrompt(s *State) bool {
	if s == nil || s.Consent != ConsentUndecided {
		return false
	}
	if HardDisabled() || ValidateEndpoint(Endpoint()) != nil {
		return false
	}
	return Interactive()
}

// Grant records consent.
func Grant(s *State, version string, now time.Time) error {
	if !validInstallID(s.InstallID) || s.ConsentEndpoint != Endpoint() || s.SchemaVersion != SchemaVersion {
		s.Counters = nil
		s.LastPayload = nil
		s.LastSentDay = ""
		id, err := newInstallID()
		if err != nil {
			return err
		}
		s.InstallID = id
	}
	s.SchemaVersion = SchemaVersion
	s.ConsentEndpoint = Endpoint()
	s.Consent = ConsentGranted
	s.ConsentVersion = version
	s.ConsentDay = dayOf(now)
	if s.Counters == nil {
		s.Counters = map[string]int{}
	}
	return nil
}

// Decline records a refusal.
func Decline(s *State, version string, now time.Time) {
	s.SchemaVersion = SchemaVersion
	s.Consent = ConsentDeclined
	s.ConsentVersion = version
	s.ConsentDay = dayOf(now)
	s.InstallID = ""
	s.ConsentEndpoint = ""
	s.LastPayload = nil
	s.LastSentDay = ""
	s.Counters = nil
}

// RotateInstallID replaces the install id with a fresh random one.
func RotateInstallID(s *State) error {
	id, err := newInstallID()
	if err != nil {
		return err
	}
	s.InstallID = id
	s.Counters = nil
	s.LastPayload = nil
	s.LastSentDay = ""
	return nil
}

// Enabled reports whether a send is currently permitted by consent and the hard-disable switches, ignoring interactivity.
func Enabled(s *State) (bool, DisableReason) {
	if r := HardDisableReason(); r != ReasonNone {
		return false, r
	}
	switch s.Consent {
	case ConsentGranted:
		if s.SchemaVersion != SchemaVersion || s.ConsentEndpoint != Endpoint() {
			return false, DisableReason("endpoint or schema changed; interactive consent required")
		}
		if !validInstallID(s.InstallID) {
			return false, DisableReason("invalid install id; interactive consent required")
		}
		return true, ReasonNone
	case ConsentDeclined:
		return false, ReasonDeclined
	default:
		return false, ReasonUndecided
	}
}

// Disable commits a fresh refusal under the send lock. It may wait for an
// in-flight request, but a completed disable is never a stale snapshot save.
func Disable(version string, now time.Time) error {
	unlock, err := lockState()
	if err != nil {
		return err
	}
	defer unlock()
	s := LoadState()
	Decline(s, version, now)
	return saveStateLocked(s)
}
