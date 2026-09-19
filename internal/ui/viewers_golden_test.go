package ui

// Golden frames for the viewers indicator: the preview panel line and the
// row badge with 0, 1 and 2 terminals attached, plus the unknown state a
// remote too old to report viewers produces. Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run 'TestViewers.*Golden'
//
// and review the diff like any other test change.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func goldenViewers(now time.Time, n int) []tmux.Viewer {
	all := []tmux.Viewer{
		{Name: "/dev/ttys004", TTY: "/dev/ttys004", Width: 200, Height: 60, User: "ashesh", Activity: now.Add(-5*time.Second - 400*time.Millisecond)},
		{Name: "/dev/pts/3", TTY: "/dev/pts/3", Width: 120, Height: 40, User: "yasir", Activity: now.Add(-3*time.Minute - 10*time.Second)},
	}
	return all[:n]
}

func assertViewersGolden(t *testing.T, name, rendered string) {
	t.Helper()
	got := strings.TrimRight(stripAnsi(rendered), "\n") + "\n"
	path := filepath.Join("testdata", "viewers", name+".txt")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s frame differs\nwant:\n%s\ngot:\n%s", name, want, got)
	}
}

// TestViewersPreview_Golden renders the classic preview header for a local
// session with the viewer cache seeded at 0, 1 and 2 viewers, and cold
// (unknown).
func TestViewersPreview_Golden(t *testing.T) {
	forceTrueColorProfile()
	tmux.ResetViewersCacheForTest()
	t.Cleanup(tmux.ResetViewersCacheForTest)

	home, inst, _ := armHomeWithOneSession(t)
	inst.Status = session.StatusWaiting
	home.embeddedLayout = false
	home.previewCache[inst.ID] = "terminal output"
	ts := inst.GetTmuxSession()
	if ts == nil {
		t.Fatal("test instance has no tmux session")
	}

	for _, step := range []struct {
		name string
		seed func()
	}{
		{"00-unknown", func() { tmux.SeedViewersCacheForTest(ts.SocketName, nil) }},
		{"01-none", func() { tmux.SeedViewersCacheForTest(ts.SocketName, map[string][]tmux.Viewer{}) }},
		{"02-one", func() {
			tmux.SeedViewersCacheForTest(ts.SocketName, map[string][]tmux.Viewer{ts.Name: goldenViewers(time.Now(), 1)})
		}},
		{"03-two", func() {
			tmux.SeedViewersCacheForTest(ts.SocketName, map[string][]tmux.Viewer{ts.Name: goldenViewers(time.Now(), 2)})
		}},
	} {
		t.Run(step.name, func(t *testing.T) {
			step.seed()
			frame := home.renderPreviewPane(90, 12)
			// Only the header block is under test; the output section below
			// it belongs to other goldens.
			lines := strings.Split(stripAnsi(frame), "\n")
			if len(lines) > 6 {
				lines = lines[:6]
			}
			assertViewersGolden(t, "preview-"+step.name, strings.Join(lines, "\n"))
		})
	}
}

// TestViewersRow_Golden renders a session row with the badge at 0, 1 and 2
// viewers: the count only, since a row has no room for names.
func TestViewersRow_Golden(t *testing.T) {
	forceTrueColorProfile()
	tmux.ResetViewersCacheForTest()
	t.Cleanup(tmux.ResetViewersCacheForTest)

	home := newSeamATestHome()
	home.width, home.height = 100, 30
	inst := session.NewInstanceWithTool("API worker", "/tmp/api", "shell")
	inst.Status = session.StatusRunning
	ts := inst.GetTmuxSession()
	if ts == nil {
		t.Fatal("test instance has no tmux session")
	}
	home.flatItems = []session.Item{{Type: session.ItemTypeSession, Session: inst}}
	home.refreshSessionRenderSnapshot([]*session.Instance{inst})

	for n := 0; n <= 2; n++ {
		tmux.SeedViewersCacheForTest(ts.SocketName, map[string][]tmux.Viewer{ts.Name: goldenViewers(time.Now(), n)})
		assertViewersGolden(t, "row-"+string(rune('0'+n)), home.renderSessionList(100, 4))
	}
}

// TestViewersRemote_Golden: a remote row and preview with viewers reported,
// with none, and from a remote too old to report them.
func TestViewersRemote_Golden(t *testing.T) {
	forceTrueColorProfile()
	withControllerVersion(t, "1.16.11")
	two := goldenViewers(time.Now(), 2)
	none := []tmux.Viewer{}
	for _, step := range []struct {
		name    string
		viewers *[]tmux.Viewer
		version string
	}{
		{"remote-two", &two, "1.16.11"},
		{"remote-none", &none, "1.16.11"},
		{"remote-older", nil, "1.16.10"},
		{"remote-silent", nil, "1.16.11"},
	} {
		t.Run(step.name, func(t *testing.T) {
			h := newSeamATestHome()
			h.width, h.height = 100, 30
			h.remoteVersions = map[string]session.RemoteVersionState{
				"lab": {Version: step.version, Found: true, CheckedAt: time.Now()},
			}
			rs := session.RemoteSessionInfo{ID: "remote", Title: "Remote worker", Tool: "claude", Status: "waiting", Path: "/srv/app", RemoteName: "lab", Viewers: step.viewers}
			item := session.Item{Type: session.ItemTypeRemoteSession, RemoteSession: &rs, RemoteName: "lab", Level: 1}
			var frame strings.Builder
			h.renderRemoteSessionItem(&frame, item, false)
			frame.WriteString("\n")
			preview := strings.Split(stripAnsi(h.renderRemotePreview(item, 90, 20)), "\n")
			if len(preview) > 8 {
				preview = preview[:8]
			}
			frame.WriteString(strings.Join(preview, "\n"))
			assertViewersGolden(t, step.name, frame.String())
		})
	}
}

// TestViewersRowWidthBudget_Golden: the viewers badge takes part in the
// row's width budget (#2201). On a narrow list a long title keeps its floor
// and the badge is what shortens (to the eye alone) and then drops, with
// the account badge going first.
func TestViewersRowWidthBudget_Golden(t *testing.T) {
	forceTrueColorProfile()
	tmux.ResetViewersCacheForTest()
	t.Cleanup(tmux.ResetViewersCacheForTest)

	for _, step := range []struct {
		name          string
		terminalWidth int
	}{
		{"width-term100", 100},
		{"width-term80", 80},
		{"width-term60", 60},
		{"width-term48", 48},
	} {
		t.Run(step.name, func(t *testing.T) {
			h := newSeamATestHome()
			h.width, h.height = step.terminalWidth, 40
			listWidth := h.sessionsPaneWidth()
			inst := session.NewInstanceWithTool("a genuinely long session title that would elide the account suffix first", "/tmp/api", "shell")
			inst.Status = session.StatusRunning
			inst.Account = "personal"
			ts := inst.GetTmuxSession()
			if ts == nil {
				t.Fatal("test instance has no tmux session")
			}
			tmux.SeedViewersCacheForTest(ts.SocketName, map[string][]tmux.Viewer{ts.Name: goldenViewers(time.Now(), 2)})
			state := sessionRenderState{
				status:         session.StatusRunning,
				tool:           "shell",
				title:          inst.Title,
				account:        inst.Account,
				accountDisplay: newAccountPresentation(inst.Account, true),
			}
			var b strings.Builder
			h.renderSessionItem(&b, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, Path: "work", IsLastInGroup: true}, false, map[string]sessionRenderState{inst.ID: state}, listWidth)
			row := strings.TrimSuffix(stripAnsi(b.String()), "\n")
			if w := cellWidth(row); w > listWidth {
				t.Errorf("row is %d cells wide, list is %d:\n%s", w, listWidth, row)
			}
			assertViewersGolden(t, step.name, "terminal_width="+strconv.Itoa(step.terminalWidth)+" list_width="+strconv.Itoa(listWidth)+"\n"+row)
		})
	}
}
