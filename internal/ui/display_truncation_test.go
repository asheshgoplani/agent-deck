package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestTruncateDisplayLeft(t *testing.T) {
	for _, tc := range []struct {
		in, head string
		width    int
		want     string
	}{
		{"/tmp/repo", "…", 20, "/tmp/repo"},
		{"/home/user/projects/repo", "…", 10, "…ects/repo"},
		{"/home/ü/项目/ünïcödé", "…", 8, "…ünïcödé"},
		{"/a/项目项目", "...", 6, "...目"},
		{"/a/项目项目", "...", 7, "...项目"},
		{"abcdef", "...", 3, "def"},
		{"abcdef", "…", 0, ""},
	} {
		got := truncateDisplayLeft(tc.in, tc.width, tc.head)
		if got != tc.want {
			t.Errorf("truncateDisplayLeft(%q, %d, %q) = %q, want %q", tc.in, tc.width, tc.head, got, tc.want)
		}
		if !utf8.ValidString(got) || lipgloss.Width(got) > max(tc.width, 0) {
			t.Errorf("truncateDisplayLeft(%q, %d) = %q: invalid UTF-8 or wider than the room", tc.in, tc.width, got)
		}
	}
}

// The hint holds multi-byte arrows and a path can be non-ASCII: neither may
// be cut inside a rune, and no line may outgrow the dialog.
func TestEditPathsDialogCutsByDisplayWidth(t *testing.T) {
	long := "/home/ünïcödé/项目/" + strings.Repeat("répertoire-", 8) + "fin"
	for width := 40; width <= 80; width++ {
		for _, editing := range []bool{false} {
			d := NewEditPathsDialog()
			d.Show(newTestMultiRepoInstance([]string{long, "/tmp/repo-b"}), nil)
			d.SetSize(width, 40)
			d.editing = editing
			view := d.View()
			if !utf8.ValidString(view) {
				t.Fatalf("width %d: view is not valid UTF-8", width)
			}
			limit := max(width-10, 40) + 2 // dialog width plus its border
			if width >= 70 {
				limit = 62
			}
			for _, line := range strings.Split(ansi.Strip(view), "\n") {
				if w := lipgloss.Width(strings.TrimSpace(line)); w > limit {
					t.Fatalf("width %d: line is %d columns, dialog is %d: %q", width, w, limit, line)
				}
			}
			if !strings.Contains(ansi.Strip(view), "fin") {
				t.Fatalf("width %d: the end of the path must stay visible:\n%s", width, ansi.Strip(view))
			}
		}
	}
}

func TestClipLeftKeepsRunes(t *testing.T) {
	got := clipLeft("/home/ünïcödé/项目/src", 10)
	if !utf8.ValidString(got) || lipgloss.Width(got) > 10 || !strings.HasPrefix(got, "...") || !strings.HasSuffix(got, "src") {
		t.Fatalf("clipLeft = %q", got)
	}
	if got := clipLeft("/项目项目", 8); !utf8.ValidString(got) || lipgloss.Width(got) > 8 {
		t.Fatalf("clipLeft cut inside a rune: %q", got)
	}
	if got := clipLeft("/short", 10); got != "/short" {
		t.Fatalf("clipLeft short = %q", got)
	}
}

func TestCostDashboardTitleCutByDisplayWidth(t *testing.T) {
	title := strings.Repeat("项目", 20)
	d := costDashboard{top: []costs.SessionCost{{SessionTitle: title, SessionID: "s1"}}}
	view := d.View()
	if !utf8.ValidString(view) {
		t.Fatal("cost dashboard view is not valid UTF-8")
	}
	want := truncateDisplay(title, 35, "...")
	if !strings.Contains(ansi.Strip(view), want) {
		t.Fatalf("title not cut to 35 columns (%q):\n%s", want, ansi.Strip(view))
	}
}

func TestMCPColumnNameCutByDisplayWidth(t *testing.T) {
	m := NewMCPDialog()
	out := m.renderColumn("Attached", []MCPItem{{Name: strings.Repeat("ünïcödé-", 6), IsOrphan: true}}, 0, true)
	if !utf8.ValidString(out) {
		t.Fatal("MCP column is not valid UTF-8")
	}
}

func TestSessionRowBadgesCutByDisplayWidth(t *testing.T) {
	h := NewHome()
	h.width, h.height = 240, 40
	h.embeddedLayout = false
	h.compactSidebar = false
	inst := &session.Instance{ID: "row", Title: "row-title", Tool: "claude", Status: session.StatusIdle,
		WorktreePath: "/tmp/wt", WorktreeBranch: "fonctionnalité-é", SSHHost: "hôte-ünïcödé.exemple"}
	var b strings.Builder
	h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, nil, 240)
	row := b.String()
	if !utf8.ValidString(row) {
		t.Fatalf("session row is not valid UTF-8: %q", row)
	}

	rs := session.RemoteSessionInfo{ID: "r1", Title: strings.Repeat("項", 30), Tool: "claude", Status: "idle", RemoteName: "dev"}
	b.Reset()
	h.renderRemoteSessionItemAtWidth(&b, session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: "dev", Level: 1, IsLastInGroup: true}, false, 240)
	if out := b.String(); !utf8.ValidString(out) {
		t.Fatalf("remote row is not valid UTF-8: %q", out)
	}
}
