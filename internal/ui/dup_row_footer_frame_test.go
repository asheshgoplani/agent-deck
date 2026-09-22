package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The 2026-09-23 v1.16.16 frame: "conductors" and its first child drawn twice
// at the top of a 200x60 deck with eight local groups, four collapsed remotes
// (sbbox selected, retry hint on), and the health budget warning in the footer.
// These tests replay successive frames of that shape through Bubble Tea's real
// renderer into a terminal model, toggling the footer and landing remote
// refreshes between frames, and check what is physically on screen.

var dupFrameRemotes = []struct {
	name string
	n    int
}{{"agentbox", 92}, {"g14", 1}, {"innobox", 46}, {"sbbox", 37}}

func dupFrameRemoteSessions(name string, n, gen int) []session.RemoteSessionInfo {
	statuses := []string{"running", "waiting", "idle", "stopped", "error"}
	tools := []string{"claude", "codex", "pi"}
	out := make([]session.RemoteSessionInfo, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, session.RemoteSessionInfo{
			ID:     fmt.Sprintf("%s-%d", name, i),
			Title:  fmt.Sprintf("%s-s%d", name, i),
			Group:  "work",
			Status: statuses[(i+gen)%len(statuses)],
			Tool:   tools[i%len(tools)],
		})
	}
	return out
}

func dupFrameHome(t *testing.T) *Home {
	t.Helper()
	forceTrueColorProfile()
	var cfg strings.Builder
	for _, r := range dupFrameRemotes {
		fmt.Fprintf(&cfg, "[remotes.%s]\nhost = \"%s.example\"\nagent_deck_path = \"/usr/local/bin/agent-deck\"\n\n", r.name, r.name)
	}
	withTempAgentDeckHome(t, cfg.String())

	specs := []struct {
		name, path string
		n          int
		expanded   bool
	}{
		{"conductors", "conductors", 4, true},
		{"My Sessions", "my-sessions", 12, false},
		{"agent-deck", "agent-deck", 15, false},
		{"tmp", "tmp", 13, false},
		{"personal", "personal", 18, false},
		{"opengraphdb", "opengraphdb", 0, false},
		{"company", "company", 1, false},
		{"watchers", "watchers", 3, false},
	}
	conductorTitles := []string{session.MaestroSessionTitle, "conductor-agent-deck", "conductor-personal", "conductor-opengraphdb"}
	statuses := []session.Status{session.StatusIdle, session.StatusRunning, session.StatusWaiting, session.StatusError}
	var groups []*session.GroupData
	var insts []*session.Instance
	for i, sp := range specs {
		groups = append(groups, &session.GroupData{Name: sp.name, Path: sp.path, Expanded: sp.expanded, Order: i})
		for j := 0; j < sp.n; j++ {
			title, tool := fmt.Sprintf("%s-s%d", sp.path, j), "claude"
			if sp.path == "conductors" {
				title = conductorTitles[j]
				if j == 0 {
					tool = "pi"
				}
			}
			inst := session.NewInstanceWithTool(title, "/tmp/"+sp.path, tool)
			inst.GroupPath = sp.path
			inst.Status = statuses[j%len(statuses)]
			insts = append(insts, inst)
		}
	}

	h := NewHome()
	t.Cleanup(h.cancel)
	h.width, h.height = 200, 60
	h.initialLoading = false
	h.instancesMu.Lock()
	h.instances = insts
	h.instancesMu.Unlock()
	h.groupTree = session.NewGroupTreeWithGroups(insts, groups)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{}
	h.remoteGroupsCollapsed = map[string]bool{}
	h.remotePolls = map[string]session.RemotePollState{}
	for _, r := range dupFrameRemotes {
		h.remoteSessions[r.name] = dupFrameRemoteSessions(r.name, r.n, 0)
		h.remoteGroupsCollapsed["remotes/"+r.name] = true
	}
	h.rebuildFlatItems()
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeRemoteGroup && it.Path == "remotes/sbbox" {
			h.cursor = i
		}
	}
	return h
}

// dupFrameRefresh lands a pushed listing for one remote, the way a remote
// poll or push refresh reaches the deck between two frames.
func dupFrameRefresh(h *Home, name string, n, gen int) {
	failed := map[string]bool{}
	for _, r := range dupFrameRemotes {
		if r.name != name {
			failed[r.name] = true
		}
	}
	h.applyRemoteFetch(remoteSessionsFetchedMsg{
		pushed:       true,
		sessions:     map[string][]session.RemoteSessionInfo{name: dupFrameRemoteSessions(name, n, gen)},
		failed:       failed,
		groupsFailed: map[string]bool{name: true},
	})
}

func dupFrameFooter(on bool) func(*Home) {
	return func(h *Home) {
		if on {
			h.healthWarningText, h.healthWarningAt = "Health: status pass exceeds 250 ms budget", time.Now()
		} else {
			h.healthWarningText = ""
		}
	}
}

type dupFrameStep struct {
	name  string
	apply func(*Home)
}

// dupFrameSequence applies each step and renders the frame after it, each
// frame tagged by sbbox's poll time so its flush can be waited for.
func dupFrameSequence(t *testing.T, h *Home, steps []dupFrameStep) (frames, markers, names []string) {
	t.Helper()
	for i, st := range steps {
		st.apply(h)
		ms := int64(1901 + i)
		h.remoteSessionsMu.Lock()
		h.remotePolls["sbbox"] = session.RemotePollState{LastPollMS: &ms, LastPollStatus: "ok"}
		h.remoteSessionsMu.Unlock()
		frames = append(frames, h.View())
		markers = append(markers, fmt.Sprintf("poll %dms", ms))
		names = append(names, st.name)
	}
	return frames, markers, names
}

// footerAndRefreshSteps: the health footer toggles and remote listings land
// between frames, with sbbox selected throughout.
var footerAndRefreshSteps = []dupFrameStep{
	{"steady", func(*Home) {}},
	{"footer on", dupFrameFooter(true)},
	{"agentbox refresh", func(h *Home) { dupFrameRefresh(h, "agentbox", 93, 1) }},
	{"footer off", dupFrameFooter(false)},
	{"local status change + footer on", func(h *Home) {
		h.instances[1].Status = session.StatusWaiting
		dupFrameFooter(true)(h)
	}},
	{"sbbox refresh + footer off", func(h *Home) {
		dupFrameRefresh(h, "sbbox", 37, 2)
		dupFrameFooter(false)(h)
	}},
	{"footer on + innobox refresh", func(h *Home) {
		dupFrameFooter(true)(h)
		dupFrameRefresh(h, "innobox", 45, 3)
	}},
	{"footer off", dupFrameFooter(false)},
}

// dupFrameDevanagari is a preview line of the #2334 kind: sized by grapheme
// cluster it fits, drawn per code point or by Ghostty 1.3 it is wider.
const dupFrameDevanagari = "\x1b[48;5;236m अच्छा, यह मेरा जो account है ना, यहाँ पे semantic वाला login है, लेकिन यह यहाँ पे buddii क्यों show कर रहा है? इसमें देख के मुझे बताओ ज़रा कि यह क्या चीज़ है, यह क्यों हो रहा है।\x1b[0m"

// selectRow parks the cursor on the row whose identity matches.
func selectRow(h *Home, match func(session.Item) bool) {
	for i, it := range h.flatItems {
		if match(it) {
			h.cursor = i
			return
		}
	}
}

// previewThenRemoteSteps: the cursor sits on a conductor session whose
// preview has complex-script lines, then moves to sbbox while the footer
// toggles and a remote listing lands, the path the reported frame was on.
var previewThenRemoteSteps = []dupFrameStep{
	{"conductor preview", func(h *Home) {
		inst := h.instances[1] // conductor-agent-deck
		h.previewCacheMu.Lock()
		h.previewCache[inst.ID] = strings.Repeat("  Ran 5 shell commands\n\n"+dupFrameDevanagari+"\n\n", 4)
		h.previewCacheTime[inst.ID] = time.Now()
		h.previewCacheMu.Unlock()
		selectRow(h, func(it session.Item) bool { return it.Session == inst })
	}},
	{"footer on", dupFrameFooter(true)},
	{"status change", func(h *Home) { h.instances[1].Status = session.StatusWaiting }},
	{"select sbbox + refresh", func(h *Home) {
		dupFrameRefresh(h, "sbbox", 37, 1)
		selectRow(h, func(it session.Item) bool {
			return it.Type == session.ItemTypeRemoteGroup && it.Path == "remotes/sbbox"
		})
	}},
	{"footer off", dupFrameFooter(false)},
	{"footer on", dupFrameFooter(true)},
}

// renderFramesPerFlush plays frames through Bubble Tea's alt-screen renderer
// and returns every write plus how many writes had landed when frame i was
// on screen.
func renderFramesPerFlush(t *testing.T, frames, markers []string, width, height int) (chunks [][]byte, upTo []int) {
	t.Helper()
	rec := &chunkRecorder{}
	p := tea.NewProgram(&framesModel{frames: frames},
		tea.WithOutput(rec), tea.WithInput(nil), tea.WithAltScreen(), tea.WithoutSignalHandler())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := p.Run(); err != nil {
			t.Errorf("bubbletea run: %v", err)
		}
	}()
	p.Send(tea.WindowSizeMsg{Width: width, Height: height})
	for i := range frames {
		p.Send(showFrameMsg(i))
		rec.waitFor(t, markers[i])
		time.Sleep(40 * time.Millisecond)
		rec.mu.Lock()
		upTo = append(upTo, len(rec.chunks))
		rec.mu.Unlock()
	}
	p.Quit()
	<-done
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.chunks, upTo
}

// dupFrameTokens are the list rows of the frame; each must be on as many
// physical screen rows as the frame itself puts it on.
var dupFrameTokens = []string{
	"conductors (", session.MaestroSessionTitle, "conductor-agent-deck", "conductor-personal", "conductor-opengraphdb",
	"My Sessions (", "tmp (", "watchers (",
	"remotes/agentbox (", "remotes/g14 (", "remotes/innobox (", "remotes/sbbox (",
	"Health: status pass",
}

func rowsContaining(rows []string, tok string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, tok) {
			n++
		}
	}
	return n
}

func frameRows(frame string) []string {
	rows := strings.Split(frame, "\n")
	for i, r := range rows {
		rows[i] = strings.TrimRight(ansi.Strip(r), " ")
	}
	return rows
}

// assertDupFrameScreens replays steps through Bubble Tea into a terminal of
// every width convention. After every frame, no list row may be on screen
// more (or fewer) times than the frame draws it, and under the convention
// the deck measures with the screen must equal the frame row for row.
func assertDupFrameScreens(t *testing.T, steps []dupFrameStep) {
	const width, height = 200, 60
	frames, markers, names := dupFrameSequence(t, dupFrameHome(t), steps)
	for i, f := range frames {
		if n := len(strings.Split(f, "\n")); n != height {
			t.Fatalf("frame %d (%s) has %d lines, want %d", i, names[i], n, height)
		}
	}
	chunks, upTo := renderFramesPerFlush(t, frames, markers, width, height)

	convs := append([]struct {
		name string
		conv widthConvention
	}{}, widthConventions...)
	convs = append(convs, struct {
		name string
		conv widthConvention
	}{"ambiguous-wide", convAmbiguousWide})

	for _, c := range convs {
		t.Run(c.name, func(t *testing.T) {
			failedAt := -1
			screens := make([][]string, len(frames))
			for i := range frames {
				term := newWrapTerm(width, height, c.conv)
				for _, ch := range chunks[:upTo[i]] {
					_, _ = term.Write(ch)
				}
				screen, want := term.rows(), frameRows(frames[i])
				screens[i] = screen
				var errs []string
				for _, tok := range dupFrameTokens {
					if got, exp := rowsContaining(screen, tok), rowsContaining(want, tok); got != exp {
						errs = append(errs, fmt.Sprintf("%q on %d screen rows, frame draws it on %d", tok, got, exp))
					}
				}
				if c.conv == convCluster {
					for r := range want {
						if screen[r] != want[r] {
							errs = append(errs, fmt.Sprintf("row %d differs:\n  screen %q\n  frame  %q", r, screen[r], want[r]))
							break
						}
					}
				}
				if len(errs) > 0 {
					t.Errorf("frame %d (%s):\n%s", i, names[i], strings.Join(errs, "\n"))
					failedAt = i
				}
			}
			if failedAt >= 0 {
				t.Logf("screen top after frame %d (%s):\n%s", failedAt, names[failedAt], strings.Join(screens[failedAt][:24], "\n"))
			}
		})
	}
}

// The footer toggling and remote refreshes on their own, sbbox selected.
func TestDupRowFrame_FooterToggleAndRemoteRefresh(t *testing.T) {
	assertDupFrameScreens(t, footerAndRefreshSteps)
}

// A conductor session's complex-script preview on screen, then the cursor
// moves to sbbox while the footer toggles: the reported frame's path.
func TestDupRowFrame_ComplexScriptPreviewThenRemoteSelected(t *testing.T) {
	assertDupFrameScreens(t, previewThenRemoteSteps)
}
