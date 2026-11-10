package ui

// The 🎙 voice badge marks the session row that holds the owner's voice mic,
// for local rows and for rows under a remote, with the voice state in words
// when the row has room ("🎙 listening", "🎙 ▶ speaking", `☾ say "jarvis"`).
//
// The data is the voice daemon's own file (internal/voicestate), never the
// title and never agent-deck state, so a crashed daemon cannot leave a badge
// behind: the reader drops a dead pid or a missed heartbeat. The tick stats
// the file (refreshVoiceState) and the rows render from h.voiceState only,
// so the render path does no IO.
//
// Matching is by id only, never by title: a local row when target.remote is
// empty and target.sessionId is the row's id (and target.profile, when set,
// is this TUI's profile); a remote row when target.remote is the row's
// remote name and the id matches. Two rows that fit means no badge.

import (
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

// voiceGroupGlyph is what a collapsed group or remote header holding the
// mic's session shows.
const voiceGroupGlyph = "🎙"

// refreshVoiceState re-reads the voice state on the tick. The reader stats
// the files and reads them only when they changed; any error reads as no
// voice.
func (h *Home) refreshVoiceState() {
	if h.voiceReader == nil {
		return
	}
	h.voiceState = h.voiceReader.Poll()
}

func (h *Home) voiceClock() time.Time {
	if h.voiceNow != nil {
		return h.voiceNow()
	}
	return time.Now()
}

// voiceProfileMatches: a target profile, when the daemon set one, must be
// the profile this TUI shows.
func (h *Home) voiceProfileMatches(target string) bool {
	if target == "" {
		return true
	}
	mine := h.profile
	if mine == "" {
		mine = session.DefaultProfile
	}
	return target == mine
}

// voiceForLocal returns the voice state when inst is the local session
// holding the mic, else nil.
func (h *Home) voiceForLocal(inst *session.Instance) *voicestate.State {
	st := h.voiceState
	if st == nil || inst == nil || st.Target.Remote != "" || st.Target.SessionID == "" || inst.ID != st.Target.SessionID {
		return nil
	}
	if !h.voiceProfileMatches(st.Target.Profile) {
		return nil
	}
	n := 0
	for _, other := range h.instances {
		if other != nil && other.ID == st.Target.SessionID {
			n++
		}
	}
	if n > 1 {
		return nil // two rows fit: no badge
	}
	return st
}

// voiceForRemote returns the voice state when the remote session id on
// remoteName is the one holding the mic, else nil.
func (h *Home) voiceForRemote(remoteName, id string) *voicestate.State {
	st := h.voiceState
	if st == nil || st.Target.Remote == "" || st.Target.SessionID == "" || remoteName != st.Target.Remote || id != st.Target.SessionID {
		return nil
	}
	h.remoteSessionsMu.RLock()
	n := 0
	for _, rs := range h.remoteSessionsInView(remoteName) {
		if rs.ID == id {
			n++
		}
	}
	h.remoteSessionsMu.RUnlock()
	if n > 1 {
		return nil
	}
	return st
}

// voiceInLocalGroup reports whether the mic's local session lives in
// groupPath or below it.
func (h *Home) voiceInLocalGroup(groupPath string) bool {
	st := h.voiceState
	if st == nil || st.Target.Remote != "" || st.Target.SessionID == "" || groupPath == "" {
		return false
	}
	inst := h.instanceByID[st.Target.SessionID]
	if inst == nil || h.voiceForLocal(inst) == nil {
		return false
	}
	return inst.GroupPath == groupPath || strings.HasPrefix(inst.GroupPath, groupPath+"/")
}

// voiceInRemoteGroup reports whether the mic's session is on remoteName
// and, for a sub-group header (groupPath != ""), inside that group.
func (h *Home) voiceInRemoteGroup(remoteName, groupPath string) bool {
	st := h.voiceState
	if st == nil || st.Target.Remote != remoteName || st.Target.SessionID == "" {
		return false
	}
	h.remoteSessionsMu.RLock()
	var match *session.RemoteSessionInfo
	n := 0
	for _, rs := range h.remoteSessionsInView(remoteName) {
		if rs.ID == st.Target.SessionID {
			n++
			rs := rs
			match = &rs
		}
	}
	h.remoteSessionsMu.RUnlock()
	if n != 1 {
		return false
	}
	if groupPath == "" {
		return true
	}
	return match.Group == groupPath || strings.HasPrefix(match.Group, groupPath+"/")
}

// voiceBadgeStyle is the badge's color: yellow while an engine falls back,
// dim while muted, resting, released, on a call or in a meeting.
func voiceBadgeStyle(tone voicestate.Tone, selected bool) lipgloss.Style {
	switch tone {
	case voicestate.ToneWarn:
		s := lipgloss.NewStyle().Foreground(ColorYellow)
		if selected {
			s = s.Bold(true) // keep the warning readable inside the highlight
		}
		return s
	case voicestate.ToneDim:
		if selected {
			return SessionStatusSelStyle
		}
		return DimStyle
	}
	if selected {
		return SessionStatusSelStyle
	}
	return lipgloss.NewStyle().Foreground(ColorAccent)
}

// fitVoiceBadge renders the row badge within budget cells (negative means
// unbounded): the words when they fit in wordsBudget too (the room left
// without cutting the title; negative means unbounded), the glyphs alone
// when they do not, nothing when even those do not fit budget. The words
// are always in the preview pane, so a row never cuts its title for them.
func fitVoiceBadge(st *voicestate.State, selected bool, budget, wordsBudget int, now time.Time) (string, int) {
	if st == nil {
		return "", 0
	}
	b := st.Badge(now)
	if b.Full == "" {
		return "", 0
	}
	text := " " + b.Full
	tooWide := (budget >= 0 && cellWidth(text) > budget) || (wordsBudget >= 0 && cellWidth(text) > wordsBudget)
	if tooWide {
		text = " " + b.Narrow
		if b.Narrow == "" || (budget >= 0 && cellWidth(text) > budget) {
			return "", 0
		}
	}
	return voiceBadgeStyle(b.Tone, selected).Render(text), cellWidth(text)
}

// voiceGroupMarker is the " 🎙" a collapsed header holding the mic shows,
// toned like the row badge.
func (h *Home) voiceGroupMarker(selected bool) string {
	b := h.voiceState.Badge(h.voiceClock())
	if b.Full == "" {
		return ""
	}
	glyph := voiceGroupGlyph
	if strings.HasPrefix(b.Narrow, "☾") {
		glyph = "☾"
	}
	return voiceBadgeStyle(b.Tone, selected).Render(" " + glyph)
}

// voicePreviewLine is the preview pane's "Voice here · <state>" line, empty
// when this session does not hold the mic.
func (h *Home) voicePreviewLine(st *voicestate.State) string {
	if st == nil {
		return ""
	}
	words := st.Words(h.voiceClock())
	if words == "" {
		return ""
	}
	line := "Voice here · " + words
	if st.Source == voicestate.SourceMirror && st.From != "" {
		line += " · from " + st.From
	}
	return line
}
