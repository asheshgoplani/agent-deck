package ui

// The 🎙 voice badge: the session row that holds the voice mic (local rows
// and rows under a remote), read from ~/.claude/voice/current.json (or the
// box's mirror.json) by internal/voicestate. See DESIGN-voice-badge.md.

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/triggers"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

var voiceTestNow = time.UnixMilli(1_794_308_004_000)

// voiceHome is a Home with one local session (and the given remote
// sessions) and the mic state set directly, as the tick would after a read.
func voiceHome(t *testing.T, st *voicestate.State, remotes map[string][]session.RemoteSessionInfo) (*Home, *session.Instance) {
	t.Helper()
	forceTrueColorProfile()
	h := newSeamATestHome()
	h.width, h.height = 120, 40
	inst := session.NewInstanceWithTool("conductor-inno", "/tmp/api", "shell")
	inst.Status = session.StatusRunning
	inst.GroupPath = "work/voice"
	other := session.NewInstanceWithTool("another one", "/tmp/api", "shell")
	other.GroupPath = "work/voice"
	h.instances = []*session.Instance{inst, other}
	h.instanceByID = map[string]*session.Instance{inst.ID: inst, other.ID: other}
	h.remoteSessions = remotes
	if st != nil && st.Target.SessionID == "LOCAL" {
		st.Target.SessionID = inst.ID
	}
	h.voiceState = st
	h.voiceNow = func() time.Time { return voiceTestNow }
	return h, inst
}

func voiceLocalRow(t *testing.T, h *Home, inst *session.Instance, listWidth int, selected bool) (plain, raw string) {
	t.Helper()
	state := sessionRenderState{status: session.StatusRunning, tool: "shell", title: inst.Title}
	var sb strings.Builder
	h.renderSessionItem(&sb, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, selected, map[string]sessionRenderState{inst.ID: state}, listWidth)
	raw = strings.TrimSuffix(sb.String(), "\n")
	plain = stripAnsi(raw)
	if listWidth > 0 {
		if w := cellWidth(plain); w > listWidth {
			t.Errorf("row is %d cells wide, list is %d:\n%s", w, listWidth, plain)
		}
	}
	return plain, raw
}

func voiceRemoteRow(t *testing.T, h *Home, rs session.RemoteSessionInfo, listWidth int) (plain, raw string) {
	t.Helper()
	item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: rs.RemoteName, Level: 1}
	var sb strings.Builder
	h.renderRemoteSessionItemAtWidth(&sb, item, false, listWidth)
	raw = strings.TrimSuffix(sb.String(), "\n")
	return stripAnsi(raw), raw
}

func micLocal(phase string) *voicestate.State {
	return &voicestate.State{On: true, Phase: phase, Target: voicestate.Target{SessionID: "LOCAL", Title: "conductor-inno"}}
}

func TestVoiceBadge_LocalRow(t *testing.T) {
	h, inst := voiceHome(t, micLocal("listening"), nil)
	row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false)
	if !strings.Contains(row, "conductor-inno 🎙 listening") {
		t.Errorf("mic row lacks the badge right after the title:\n%s", row)
	}
	other := h.instances[1]
	if row, _ := voiceLocalRow(t, h, other, h.sessionsPaneWidth(), false); strings.Contains(row, "🎙") {
		t.Errorf("a row that does not hold the mic shows the badge:\n%s", row)
	}
}

func TestVoiceBadge_NoStateNoBadge(t *testing.T) {
	h, inst := voiceHome(t, nil, nil)
	if row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false); strings.Contains(row, "🎙") || strings.Contains(row, "☾") {
		t.Errorf("no voice state, row shows a badge:\n%s", row)
	}
}

// A target with a remote never lights a local row with the same id, and a
// local row is never matched by title.
func TestVoiceBadge_LocalRowNeedsEmptyRemoteAndID(t *testing.T) {
	st := micLocal("listening")
	st.Target.Remote = "innobox"
	h, inst := voiceHome(t, st, nil)
	if row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false); strings.Contains(row, "🎙") {
		t.Errorf("remote target lit the local row:\n%s", row)
	}
	byTitle := &voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: "someone-else", Title: "conductor-inno"}}
	h, inst = voiceHome(t, byTitle, nil)
	if row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false); strings.Contains(row, "🎙") {
		t.Errorf("matched by title:\n%s", row)
	}
}

// target.profile, when set, must be this TUI's profile.
func TestVoiceBadge_LocalRowProfile(t *testing.T) {
	st := micLocal("listening")
	st.Target.Profile = "work"
	h, inst := voiceHome(t, st, nil)
	h.profile = "personal"
	if row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false); strings.Contains(row, "🎙") {
		t.Errorf("other profile's target lit this row:\n%s", row)
	}
	h.profile = "work"
	if row, _ := voiceLocalRow(t, h, inst, h.sessionsPaneWidth(), false); !strings.Contains(row, "🎙") {
		t.Errorf("same profile's target did not light the row:\n%s", row)
	}
}

func TestVoiceBadge_RemoteRow(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "cf9a73f3-1789471273", Title: "conductor-inno-ashesh", Tool: "claude", Status: "waiting", RemoteName: "innobox"}
	twin := rs
	twin.RemoteName = "agentbox"
	st := &voicestate.State{On: true, Phase: "speaking", Target: voicestate.Target{SessionID: rs.ID, Remote: "innobox"}}
	h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{"innobox": {rs}, "agentbox": {twin}})
	row, _ := voiceRemoteRow(t, h, rs, 100)
	if !strings.Contains(row, "conductor-inno-ashesh 🎙 ▶ speaking") {
		t.Errorf("remote mic row lacks the badge:\n%s", row)
	}
	if row, _ := voiceRemoteRow(t, h, twin, 100); strings.Contains(row, "🎙") {
		t.Errorf("same id on another remote shows the badge:\n%s", row)
	}
	// An empty target.remote never lights a remote row.
	h.voiceState = &voicestate.State{On: true, Phase: "speaking", Target: voicestate.Target{SessionID: rs.ID}}
	if row, _ := voiceRemoteRow(t, h, rs, 100); strings.Contains(row, "🎙") {
		t.Errorf("local target lit a remote row:\n%s", row)
	}
}

// Two rows fit: no badge.
func TestVoiceBadge_AmbiguousRemoteNoBadge(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "dup", Title: "one", Status: "waiting", RemoteName: "innobox"}
	rs2 := session.RemoteSessionInfo{ID: "dup", Title: "two", Status: "waiting", RemoteName: "innobox"}
	st := &voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: "dup", Remote: "innobox"}}
	h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{"innobox": {rs, rs2}})
	if row, _ := voiceRemoteRow(t, h, rs, 100); strings.Contains(row, "🎙") {
		t.Errorf("ambiguous target lit a row:\n%s", row)
	}
}

// Narrow rows show the glyph only, then drop it; the title keeps its floor.
func TestVoiceBadge_NarrowRow(t *testing.T) {
	h, inst := voiceHome(t, micLocal("speaking"), nil)
	inst.Title = "a genuinely long session title that needs the room"
	wide, _ := voiceLocalRow(t, h, inst, 120, false)
	if !strings.Contains(wide, "🎙 ▶ speaking") {
		t.Errorf("wide row lacks the words:\n%s", wide)
	}
	narrow, _ := voiceLocalRow(t, h, inst, 34, false)
	if !strings.Contains(narrow, " 🎙▶") || strings.Contains(narrow, "speaking") {
		t.Errorf("narrow row should show the glyph only:\n%s", narrow)
	}
	tiny, _ := voiceLocalRow(t, h, inst, 22, false)
	if strings.Contains(tiny, "🎙") {
		t.Errorf("tiny row kept the badge over the title floor:\n%s", tiny)
	}
}

// The row never cuts its title for the words (they are in the preview
// pane): a title that needs the room keeps it and the badge shows the glyph,
// and a long title still truncates to keep the glyph (dropped last).
func TestVoiceBadge_WordsNeverCutTheTitle(t *testing.T) {
	h, inst := voiceHome(t, micLocal("listening"), nil)
	inst.Title = "conductor-inno"
	row, _ := voiceLocalRow(t, h, inst, 34, false)
	if !strings.Contains(row, "conductor-inno 🎙") || strings.Contains(row, "listening") {
		t.Errorf("want the full title and the glyph only:\n%s", row)
	}
	inst.Title = strings.Repeat("x", 80)
	row, _ = voiceLocalRow(t, h, inst, 70, false)
	if !strings.Contains(row, "… 🎙") || strings.Contains(row, "listening") {
		t.Errorf("long title: want it cut with the glyph kept:\n%s", row)
	}
}

func TestVoiceBadge_Styles(t *testing.T) {
	h, inst := voiceHome(t, micLocal("muted"), nil)
	h.voiceState.Muted = true
	_, raw := voiceLocalRow(t, h, inst, 120, false)
	if want := DimStyle.Render(" 🎙 ⏸ muted"); !strings.Contains(raw, want) {
		t.Errorf("muted badge not dim:\n%q\nwant %q", raw, want)
	}
	h.voiceState = micLocal("speaking")
	h.voiceState.Target.SessionID = inst.ID
	h.voiceState.FallingBack, h.voiceState.TTS, h.voiceState.TTSEffective = true, "local", "qwen"
	_, raw = voiceLocalRow(t, h, inst, 120, false)
	if want := lipgloss.NewStyle().Foreground(ColorYellow).Render(" 🎙 ▶ speaking ⚠ qwen"); !strings.Contains(raw, want) {
		t.Errorf("falling-back badge not yellow:\n%q\nwant %q", raw, want)
	}
}

func TestVoiceBadge_CollapsedGroupHeader(t *testing.T) {
	h, _ := voiceHome(t, micLocal("listening"), nil)
	g := &session.Group{Name: "work", Path: "work", Expanded: false}
	render := func(g *session.Group) string {
		var sb strings.Builder
		h.renderGroupItem(&sb, session.Item{Type: session.ItemTypeGroup, Group: g, Path: g.Path}, false, 0, map[string]groupRenderStats{}, h.sessionsPaneWidth())
		return stripAnsi(sb.String())
	}
	if row := render(g); !strings.Contains(row, "🎙") {
		t.Errorf("collapsed group holding the mic lacks 🎙:\n%s", row)
	}
	g.Expanded = true
	if row := render(g); strings.Contains(row, "🎙") {
		t.Errorf("expanded group repeats 🎙 (its row already shows it):\n%s", row)
	}
	other := &session.Group{Name: "elsewhere", Path: "elsewhere"}
	if row := render(other); strings.Contains(row, "🎙") {
		t.Errorf("a group without the mic shows 🎙:\n%s", row)
	}
	// "work/voiceover" is not inside "work/voice".
	h.instances[0].GroupPath = "work/voiceover"
	sib := &session.Group{Name: "voice", Path: "work/voice"}
	if row := render(sib); strings.Contains(row, "🎙") {
		t.Errorf("prefix-sibling group shows 🎙:\n%s", row)
	}
}

func TestVoiceBadge_CollapsedRemoteHeader(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "cf9a73f3-1789471273", Title: "conductor-inno-ashesh", Status: "waiting", Group: "conductors", RemoteName: "innobox"}
	st := &voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: rs.ID, Remote: "innobox"}}
	h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{"innobox": {rs}})
	render := func(item session.Item) string {
		var sb strings.Builder
		h.renderRemoteGroupItem(&sb, item, false, 120)
		return stripAnsi(sb.String())
	}
	host := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "innobox", Path: "remotes/innobox"}
	sub := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "innobox", Path: "remotes/innobox/conductors", Level: 1}
	if row := render(host); strings.Contains(row, "🎙") {
		t.Errorf("expanded remote header shows 🎙:\n%s", row)
	}
	h.remoteGroupsCollapsed = map[string]bool{"remotes/innobox": true, "remotes/innobox/conductors": true}
	if row := render(host); !strings.Contains(row, "🎙") {
		t.Errorf("collapsed remote header holding the mic lacks 🎙:\n%s", row)
	}
	if row := render(sub); !strings.Contains(row, "🎙") {
		t.Errorf("collapsed remote sub-group holding the mic lacks 🎙:\n%s", row)
	}
	other := session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "agentbox", Path: "remotes/agentbox"}
	h.remoteGroupsCollapsed["remotes/agentbox"] = true
	if row := render(other); strings.Contains(row, "🎙") {
		t.Errorf("another remote's header shows 🎙:\n%s", row)
	}
}

func TestVoiceBadge_PreviewLine(t *testing.T) {
	h, inst := voiceHome(t, micLocal("thinking"), nil)
	h.voiceState.PhaseSince = voiceTestNow.Add(-4 * time.Second).UnixMilli()
	if got := h.voicePreviewLine(h.voiceForLocal(inst)); got != "Voice here · thinking 4s" {
		t.Errorf("preview line = %q", got)
	}
	if got := h.voicePreviewLine(nil); got != "" {
		t.Errorf("no mic: preview line = %q, want empty", got)
	}
}

// The tick refreshes the state through the reader; a nil reader is a no-op.
func TestVoiceBadge_RefreshNilReader(t *testing.T) {
	h, _ := voiceHome(t, micLocal("listening"), nil)
	h.voiceReader = nil
	h.refreshVoiceState()
	if h.voiceState == nil {
		t.Error("nil reader cleared the state")
	}
	h.voiceReader = voicestate.NewReader(t.TempDir())
	h.refreshVoiceState()
	if h.voiceState != nil {
		t.Error("empty voice dir left a stale state")
	}
}

// Review finding: the words must not cut the title once the other badges
// (viewers, ⚡, account) take their share; the glyph is used instead.
func TestVoiceBadge_WordsNeverCutTitleWithOtherBadges(t *testing.T) {
	h, inst := voiceHome(t, micLocal("listening"), nil)
	h.triggerBadges = map[string]triggers.Badge{inst.ID: {Count: 3}}
	for w := 24; w <= 80; w++ {
		row, _ := voiceLocalRow(t, h, inst, w, false)
		if strings.Contains(row, "listening") && !strings.Contains(row, "conductor-inno ") {
			t.Errorf("width %d: words cut the title:\n%s", w, row)
		}
		if strings.Contains(row, "⚡") && !strings.Contains(row, "🎙") {
			t.Errorf("width %d: ⚡ kept while 🎙 dropped (voice is dropped last):\n%s", w, row)
		}
	}
}

// Review finding: on a remote row the voice badge is fitted before the
// tool, viewers and pending badges, so it is the last one to go.
func TestVoiceBadge_RemoteRowBadgeDroppedLast(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "r1", Title: "conductor-inno", Tool: "claude", Status: "waiting", RemoteName: "innobox"}
	st := &voicestate.State{On: true, Phase: "speaking", Target: voicestate.Target{SessionID: "r1", Remote: "innobox"}}
	h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{"innobox": {rs}})
	row, _ := voiceRemoteRow(t, h, rs, 30)
	if !strings.Contains(row, "conductor-inno 🎙▶") {
		t.Errorf("narrow remote row dropped the voice badge for the tool name:\n%s", row)
	}
}

// Review finding: an ungrouped remote session sits under the default group
// header; the collapsed header must still show 🎙.
func TestVoiceBadge_CollapsedRemoteDefaultGroup(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "r1", Title: "t", Status: "waiting", Group: "", RemoteName: "innobox"}
	st := &voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: "r1", Remote: "innobox"}}
	h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{"innobox": {rs}})
	path := "remotes/innobox/" + session.DefaultGroupPath
	h.remoteGroupsCollapsed = map[string]bool{path: true}
	var sb strings.Builder
	h.renderRemoteGroupItem(&sb, session.Item{Type: session.ItemTypeRemoteGroup, RemoteName: "innobox", Path: path, Level: 1}, false, 120)
	if !strings.Contains(stripAnsi(sb.String()), "🎙") {
		t.Errorf("collapsed default-group header lacks 🎙:\n%s", stripAnsi(sb.String()))
	}
}

// The header glyph is 🎙 in every state (resting included), toned like the
// row badge.
func TestVoiceBadge_CollapsedHeaderRestingShowsMic(t *testing.T) {
	jarvis := "jarvis"
	st := micLocal("resting")
	st.WakePhrase = &jarvis
	h, _ := voiceHome(t, st, nil)
	var sb strings.Builder
	h.renderGroupItem(&sb, session.Item{Type: session.ItemTypeGroup, Group: &session.Group{Name: "work", Path: "work"}, Path: "work"}, false, 0, map[string]groupRenderStats{}, 120)
	if got := sb.String(); !strings.Contains(got, DimStyle.Render(" 🎙")) {
		t.Errorf("resting collapsed header = %q, want a dim 🎙", stripAnsi(got))
	}
}
