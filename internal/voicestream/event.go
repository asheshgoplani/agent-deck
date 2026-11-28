// Package voicestream turns the voice daemon's state file into bus frames
// (docs/events.md, kinds voice.state and voice.result) so every screen
// follows one stream: `events follow`, `remote <name> events follow` and
// the web route GET /events/state.
//
// A frame carries only what screens draw. Heard and spoken text never
// leaves the daemon's machine: the producer reads voicestate.State, which
// does not decode them.
package voicestream

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// Bus kinds.
const (
	KindState  = "voice.state"
	KindResult = "voice.result"
)

// Reasons a voice.state frame was published.
const (
	ReasonStart   = "start"   // a producer started (or took over) and states what it sees
	ReasonChange  = "change"  // something a screen draws changed
	ReasonExpired = "expired" // the daemon died, stopped beating or turned off: published as "off"
	ReasonStopped = "stopped" // the producer stopped and nobody may follow it: published as "off"
)

// Target is the session the mic is on. Host "" is the host whose bus
// carries the frame; otherwise that host's name for the remote.
type Target struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Host    string `json:"host"`
	Profile string `json:"profile"`
}

// StateEvent is the data of a voice.state frame (FRAME.md).
type StateEvent struct {
	Host         string  `json:"host"`
	On           bool    `json:"on"`
	Phase        string  `json:"phase"`
	Extra        string  `json:"extra"`
	RawPhase     string  `json:"rawPhase"`
	PhaseSince   string  `json:"phaseSince"`
	PhaseSinceMs int64   `json:"phaseSinceMs"`
	Muted        bool    `json:"muted"`
	MicReleased  bool    `json:"micReleased"`
	Hibernating  bool    `json:"hibernating"`
	FallingBack  bool    `json:"fallingBack"`
	Target       *Target `json:"target"`
	// Switching: the mic is moving there (daemon v1.3); a hint for
	// "Switching to X…" only, never the holder. null = not switching.
	Switching *Target `json:"switching"`
	Source    string  `json:"source"`
	From      string  `json:"from"`
	Origin    string  `json:"origin"`
	Reason    string  `json:"reason"`
	UpdatedAt string  `json:"updatedAt"`
}

// SessionID is the frame's session_id: the target when it is on this host.
func (e StateEvent) SessionID() string {
	if e.Target != nil && e.Target.Host == "" {
		return e.Target.ID
	}
	return ""
}

// SessionIDFor is SessionID on profile's bus: "" when the mic is on a
// session of another profile. The voice state is per machine and every
// profile's producer publishes it, so profile A's bus must not name
// profile B's session as its session_id (followers check target.profile).
func (e StateEvent) SessionIDFor(profile string) string {
	if e.Target != nil && e.Target.Profile != "" && profile != "" && e.Target.Profile != profile {
		return ""
	}
	return e.SessionID()
}

// key is what a screen draws; a frame is published when it changes.
func (e StateEvent) key() string {
	var b strings.Builder
	b.WriteString(e.Phase + "|" + e.Extra + "|" + e.RawPhase + "|" + e.Source + "|" + e.From + "|")
	for _, f := range []bool{e.On, e.Muted, e.MicReleased, e.Hibernating, e.FallingBack} {
		if f {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	b.WriteString("|" + itoa(e.PhaseSinceMs))
	if t := e.Target; t != nil {
		b.WriteString("|" + t.ID + "|" + t.Title + "|" + t.Host + "|" + t.Profile)
	}
	if w := e.Switching; w != nil {
		b.WriteString("|>" + w.ID + "|" + w.Title + "|" + w.Host)
	}
	return b.String()
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// rfc3339ms is UTC RFC 3339 with milliseconds.
func rfc3339ms(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

// Off is the frame for no live voice.
func Off(reason, origin string, now time.Time) StateEvent {
	return StateEvent{Phase: voicestate.PhaseOff, From: "none", Origin: origin, Reason: reason, UpdatedAt: rfc3339ms(now)}
}

// FromState maps a polled state (nil = no live voice) to a frame.
// origin names the daemon's machine for a local current.json.
func FromState(s *voicestate.State, origin, reason string, now time.Time) StateEvent {
	if s == nil || !s.On || s.PhaseWord() == voicestate.PhaseOff {
		return Off(reason, origin, now)
	}
	e := StateEvent{
		On:          true,
		Phase:       s.PhaseWord(),
		Extra:       s.Extra(),
		RawPhase:    s.RawPhase(),
		Muted:       s.Muted,
		MicReleased: s.MicReleased,
		Hibernating: s.Hibernating,
		FallingBack: s.FallingBack,
		Source:      s.ClientSource,
		From:        "local",
		Origin:      origin,
		Reason:      reason,
		UpdatedAt:   rfc3339ms(now),
	}
	if s.Source == voicestate.SourceMirror {
		e.From = "mirror"
		if s.From != "" {
			e.Origin = s.From
		}
	}
	if s.UpdatedAt > 0 {
		e.UpdatedAt = rfc3339ms(time.UnixMilli(s.UpdatedAt))
	}
	if s.PhaseSince > 0 {
		e.PhaseSinceMs = s.PhaseSince
		e.PhaseSince = rfc3339ms(time.UnixMilli(s.PhaseSince))
	}
	if t := s.Target; t.SessionID != "" || t.Title != "" {
		e.Target = &Target{ID: t.SessionID, Title: t.Title, Host: t.Remote, Profile: t.Profile}
	}
	if w := s.Switching; w != nil && (w.SessionID != "" || w.Title != "") {
		e.Switching = &Target{ID: w.SessionID, Title: w.Title, Host: w.Remote, Profile: w.Profile}
	}
	return e
}

// LocalOrigin is this machine's name for frames from its own current.json:
// $AGENTDECK_HOSTNAME, else the short hostname.
func LocalOrigin() string {
	if v := strings.TrimSpace(os.Getenv("AGENTDECK_HOSTNAME")); v != "" {
		return v
	}
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	return h
}
