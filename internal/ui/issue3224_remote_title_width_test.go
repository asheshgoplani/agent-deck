package ui

// #3224: a classic remote session row cut its title at a fixed 25 columns,
// so a 34-column title showed as "remote-session-title-0..." on a list with
// room to spare, while a local row with the same title showed it whole. The
// title now takes the room the list leaves after the row's indent, tree
// connector, status glyph and badges, like a local row.

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/voicestate"
)

const issue3224Title = "remote-session-title-0123456789-tail" // 36 columns

// issue3224RemoteRow renders one classic remote session row and returns it
// without styling or the trailing newline.
func issue3224RemoteRow(t *testing.T, h *Home, rs session.RemoteSessionInfo, item session.Item, selected bool, listWidth int) string {
	t.Helper()
	item.Type = session.ItemTypeRemoteSession
	item.RemoteSession = &rs
	item.RemoteName = rs.RemoteName
	var b strings.Builder
	h.renderRemoteSessionItemAtWidth(&b, item, selected, listWidth)
	row := strings.TrimSuffix(b.String(), "\n")
	if strings.Contains(row, "\n") {
		t.Fatalf("classic remote row spans more than one line: %q", row)
	}
	if !utf8.ValidString(row) {
		t.Fatalf("remote row is not valid UTF-8: %q", row)
	}
	return stripAnsi(row)
}

func TestIssue3224_RemoteTitleUsesListWidth(t *testing.T) {
	two := goldenViewers(time.Now(), 2)
	tests := []struct {
		name      string
		title     string
		level     int
		sub       bool
		selected  bool
		pending   string
		viewers   bool
		voice     bool
		listWidth int
		// wantTitle is the title text the row must show. Empty means the
		// title is cut: it must end in "..." and fill the row to listWidth-1.
		wantTitle string
	}{
		{name: "wide list shows the whole title", title: issue3224Title, level: 2, listWidth: 64, wantTitle: issue3224Title},
		{name: "wider list shows the whole title", title: issue3224Title, level: 2, listWidth: 85, wantTitle: issue3224Title},
		{name: "selected row shows the whole title", title: issue3224Title, level: 2, selected: true, listWidth: 64, wantTitle: issue3224Title},
		{name: "deep sub-session shows the whole title", title: issue3224Title, level: 6, sub: true, listWidth: 64, wantTitle: issue3224Title},
		{name: "row with every badge shows the whole title", title: issue3224Title, level: 2, pending: "restarting", viewers: true, voice: true, listWidth: 120, wantTitle: issue3224Title},
		{name: "narrower list cuts the title to fit", title: issue3224Title, level: 2, listWidth: 50},
		{name: "narrower selected list cuts the title to fit", title: issue3224Title, level: 2, selected: true, listWidth: 50},
		{name: "deep sub-session cuts the title to fit", title: issue3224Title, level: 6, sub: true, listWidth: 56},
		{name: "badges take their room before the title", title: issue3224Title, level: 2, pending: "restarting", viewers: true, listWidth: 64},
		{name: "voice glyph keeps its room before the title", title: issue3224Title, level: 2, voice: true, listWidth: 50},
		{name: "wide runes are cut by display width", title: strings.Repeat("項", 30), level: 2, listWidth: 64},
		{name: "short title is never padded or cut", title: "worker", level: 2, listWidth: 64, wantTitle: "worker"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rs := session.RemoteSessionInfo{ID: "r-3224", Title: tc.title, Tool: "shell", Status: "idle", RemoteName: "lab"}
			var st *voicestate.State
			if tc.voice {
				st = &voicestate.State{On: true, Phase: "listening", Target: voicestate.Target{SessionID: rs.ID, Remote: rs.RemoteName}}
			}
			h, _ := voiceHome(t, st, map[string][]session.RemoteSessionInfo{rs.RemoteName: {rs}})
			h.embeddedLayout = false
			if tc.pending != "" {
				h.remotePending = map[string]string{rs.ID: tc.pending}
			}
			if tc.viewers {
				rs.Viewers = &two
			}
			item := session.Item{Level: tc.level, Path: "remotes/lab/work", IsLastInGroup: true, IsSubSession: tc.sub, IsLastSubSession: tc.sub}
			row := issue3224RemoteRow(t, h, rs, item, tc.selected, tc.listWidth)

			if tc.listWidth > 0 {
				if w := cellWidth(row); w > tc.listWidth {
					t.Fatalf("row is %d cells wide, list is %d:\n%s", w, tc.listWidth, row)
				}
			}
			if tc.pending != "" && !strings.Contains(row, "· "+tc.pending) {
				t.Errorf("row lost its pending badge:\n%s", row)
			}
			if tc.viewers && !strings.Contains(row, viewersEye) {
				t.Errorf("row lost its viewers badge:\n%s", row)
			}
			if tc.voice && !strings.Contains(row, "🎙") {
				t.Errorf("row lost its voice badge:\n%s", row)
			}
			if tc.wantTitle != "" {
				if !strings.Contains(row, tc.wantTitle) || strings.Contains(row, "...") {
					t.Fatalf("row does not show the whole title %q:\n%s", tc.wantTitle, row)
				}
				return
			}
			if !strings.Contains(row, "...") {
				t.Fatalf("row should cut the title with \"...\":\n%s", row)
			}
			// A cut title uses all the room it has: the row fills the list
			// up to the one-column right margin, never a fixed 25 columns. A
			// wide rune that does not fit whole may leave one more cell.
			if w := cellWidth(row); w < tc.listWidth-2 {
				t.Errorf("cut row is %d cells wide, want %d or %d (list %d minus the margin):\n%s", w, tc.listWidth-2, tc.listWidth-1, tc.listWidth, row)
			}
		})
	}
}

// A list too narrow for the whole row, or a row rendered with no width to
// fit, cuts the title exactly as before: at 25 columns, never shorter.
func TestIssue3224_RemoteTitleKeepsOldCutWhenRowDoesNotFit(t *testing.T) {
	rs := session.RemoteSessionInfo{ID: "r-3224", Title: issue3224Title, Tool: "shell", Status: "idle", RemoteName: "lab"}
	h, _ := voiceHome(t, nil, map[string][]session.RemoteSessionInfo{rs.RemoteName: {rs}})
	h.embeddedLayout = false
	want := "remote-session-title-0... shell"
	for _, listWidth := range []int{0, 20, 43} {
		row := issue3224RemoteRow(t, h, rs, session.Item{Level: 2, Path: "remotes/lab/work", IsLastInGroup: true}, false, listWidth)
		if !strings.Contains(row, want) {
			t.Errorf("list width %d: want the 25-column title %q:\n%s", listWidth, want, row)
		}
	}
}

// The issue's control: on the same list width, a local row and a remote row
// with the same title both show it whole.
func TestIssue3224_RemoteTitleMatchesLocalRow(t *testing.T) {
	const listWidth = 64
	rs := session.RemoteSessionInfo{ID: "r-3224", Title: issue3224Title, Tool: "shell", Status: "idle", RemoteName: "lab"}
	h, inst := voiceHome(t, nil, map[string][]session.RemoteSessionInfo{rs.RemoteName: {rs}})
	h.embeddedLayout = false
	inst.Title = issue3224Title
	local, _ := voiceLocalRow(t, h, inst, listWidth, false)
	remote := issue3224RemoteRow(t, h, rs, session.Item{Level: 2, Path: "remotes/lab/work", IsLastInGroup: true}, false, listWidth)
	if !strings.Contains(local, issue3224Title) {
		t.Fatalf("local control row does not show the whole title:\n%s", local)
	}
	if !strings.Contains(remote, issue3224Title) {
		t.Fatalf("remote row cuts a title the local row shows whole:\nlocal:  %s\nremote: %s", local, remote)
	}
}
