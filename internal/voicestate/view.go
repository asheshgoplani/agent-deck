package voicestate

import "time"

// View is the `voice state --json` object (desk contract section 6).
type View struct {
	On         bool        `json:"on"`
	Phase      string      `json:"phase"`
	PhaseSince string      `json:"phaseSince"`
	Extra      string      `json:"extra"` // muted|call|meeting|"": why phase is resting
	Muted      bool        `json:"muted"`
	Target     *ViewTarget `json:"target"`
	Source     string      `json:"source"`
	From       string      `json:"from"`
	// Origin is the machine of the daemon as frames name it (voice.state
	// "origin"): a mirror's "from"; "" for this machine's own file or none.
	Origin    string `json:"origin"`
	UpdatedAt string `json:"updatedAt"`
}

// ViewTarget is the session the mic is on; Host "" means this machine.
type ViewTarget struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Host    string `json:"host"`
	Profile string `json:"profile"`
}

// NewView builds the contract object from a polled state (nil = no live voice).
func NewView(s *State, now time.Time) View {
	// A state whose mapped phase is "off" is no live voice: never on:true
	// together with phase "off".
	if s == nil || !s.On || s.PhaseWord() == PhaseOff {
		return View{Phase: PhaseOff, From: "none", UpdatedAt: rfc3339(now)}
	}
	v := View{
		On:         true,
		Phase:      s.PhaseWord(),
		Extra:      s.Extra(),
		Muted:      s.Muted || s.Extra() == ExtraMuted,
		From:       "local",
		UpdatedAt:  rfc3339(now),
		PhaseSince: "",
	}
	if s.Source == SourceMirror {
		v.From = "mirror"
		v.Origin = s.From
	}
	if s.UpdatedAt > 0 {
		v.UpdatedAt = rfc3339(time.UnixMilli(s.UpdatedAt))
	}
	if s.PhaseSince > 0 {
		v.PhaseSince = rfc3339(time.UnixMilli(s.PhaseSince))
	}
	if t := s.Target; t.SessionID != "" || t.Title != "" {
		v.Target = &ViewTarget{ID: t.SessionID, Title: t.Title, Host: t.Remote, Profile: t.Profile}
	}
	return v
}

func rfc3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }
