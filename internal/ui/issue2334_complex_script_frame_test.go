package ui

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// complexScriptCorpus is the #2334 width corpus: the issue's sample, the
// lines visible in the field screenshots, and one sample per script class
// where the grapheme-cluster and per-code-point conventions disagree.
var complexScriptCorpus = []struct{ name, text string }{
	{"issue sample", "का मकी बा त, सी धी 🙂"},
	{"screenshot line A", "अच्छा, यह मेरा जो account है ना, यहाँ पे Sharjeel semantic वाला login है, लेकिन यह यहाँ पे Sharjeel buddii क्यों show कर रहा है?"},
	{"screenshot line B", "Conductor के अंदर ये Sharjeel buddii show कर रहा है, तो ये क्यों show हो रहा है?"},
	{"screenshot line C", "इसमें देख के मुझे बताओ ज़रा कि यह क्या चीज़ है, यह क्यों हो रहा है।"},
	{"devanagari conjuncts", "क्ष त्र ज्ञ श्र क्‍ष क्‌ष क़"},
	{"bengali", "আমি বাংলায় গান গাই"},
	{"tamil", "தமிழ் நாடு"},
	{"arabic", "مرحبا بالعالم آپ کیسے ہیں"},
	{"thai", "สวัสดีครับ ภาษาไทย"},
	{"emoji zwj", "👨‍👩‍👧‍👦 🏳️‍🌈 👩🏽‍💻 ❤️ 1️⃣"},
	{"hangul jamo", "한글 한글"},
}

// Every corpus sample, repeated past the pane and placed after a list-like
// left column, must come out of the final viewport clamp as a row that fits
// the pane on a terminal of ANY width convention (#2334). Before the fix
// the clamp padded by grapheme width only, so a Devanagari row sized to 80
// cells took up to 90 on a per-code-point terminal and auto-wrapped.
func TestClampViewToViewport_ComplexScriptRowsFitBothConventions_Issue2334(t *testing.T) {
	for _, sample := range complexScriptCorpus {
		for _, width := range []int{20, 41, 80, 120, 200} {
			t.Run(fmt.Sprintf("%s/w%d", sample.name, width), func(t *testing.T) {
				row := "\x1b[38;5;2m│ ● session-name\x1b[0m │ " + strings.Repeat(sample.text+" ", 1+2*width/max(1, cellWidth(sample.text)))
				out := clampViewToViewport(row+"\nnext row", width, 2)
				if got := strings.Count(out, "\n") + 1; got != 2 {
					t.Fatalf("clamp produced %d rows, want 2", got)
				}
				assertNoOverwideRows(t, out, width)
				// The row still carries content: the fit cuts, it does not blank.
				if !strings.Contains(ansi.Strip(out), "session-name") {
					t.Fatalf("row lost its content: %q", ansi.Strip(out))
				}
			})
		}
	}
}

// Every final row switches auto-wrap off before its content and back on
// after it, so a row that is still too wide for some terminal clips at the
// margin instead of wrapping, and the terminal is in auto-wrap mode between
// any two writes (#2334).
func TestClampViewToViewport_AutoWrapOffPerRow_Issue2334(t *testing.T) {
	out := clampViewToViewport("plain\n"+complexScriptCorpus[1].text+"\n", 40, 3)
	for i, row := range strings.Split(out, "\n") {
		off := strings.Index(row, ansi.ResetModeAutoWrap)
		on := strings.LastIndex(row, ansi.SetModeAutoWrap)
		if off < 0 || on < 0 || off > on || strings.Count(row, ansi.ResetModeAutoWrap) != 1 {
			t.Fatalf("row %d does not bracket its content with DECAWM off/on: %q", i, row)
		}
		if visible := ansi.Strip(row[:off] + row[on:]); visible != "" {
			t.Fatalf("row %d has content outside the DECAWM bracket: %q", i, visible)
		}
	}
}

const devanagariLastResponse = `  - Handover 05:00Z par hai.
  - Worker ko ye bhi likha ke Yasir ki side ko diye hue emulator ko na chhere.

` + "\x1b[48;5;236m अच्छा, यह मेरा जो account है ना, यहाँ पे Sharjeel semantic वाला login है, लेकिन यह यहाँ पे Sharjeel buddii क्यों show कर रहा है?Conductor के अंदर ये Sharjeel buddii show कर रहा है\x1b[0m" + `

` + "\x1b[48;5;236m इसमें देख के मुझे बताओ ज़रा कि यह क्या चीज़ है, यह क्यों हो रहा है।\x1b[0m" + `

  Ran 5 shell commands

● Maine check kar liya. Aap ka conductor semantic login par hi chal raha hai, buddii par nahi.
  Asal login kaunsa hai
  - Ye conductor: sharjeel-semantic, yani login sharjeel semantic.
  - Mere workers: do chalte hue workers ke process khud dekhe. Dono isi semantic login par chal rahe hain.`

// devanagariPreviewHome is the field screenshot's shape at 200x50: a remote
// host with a long session list, the selected remote session's last response
// full of Devanagari next to it.
func devanagariPreviewHome(t *testing.T, status, tag string) *Home {
	t.Helper()
	home := goldenRemotePreviewHome(t)
	home.width, home.height = 200, 50
	var sessions []session.RemoteSessionInfo
	for i := 0; i < 30; i++ {
		sessions = append(sessions, session.RemoteSessionInfo{
			ID: fmt.Sprintf("r%02d", i), Title: fmt.Sprintf("Sharjeel-r%d-bug%d", 3+i%4, 35+i), Group: "buddi/sharjeel/buddii",
			Status: "idle", Tool: "claude", Path: "/home/ashesh/.local/share/agent-deck/conductor/sharjeel",
		})
	}
	sessions[0].Title, sessions[0].Status = "conductor-buddi-sharjeel", status
	home.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": sessions}
	home.flatItems = []session.Item{{Type: session.ItemTypeRemoteGroup, RemoteName: "lab", Path: "remotes/lab", Level: 0}}
	for i := range sessions {
		home.flatItems = append(home.flatItems, session.Item{
			Type: session.ItemTypeRemoteSession, RemoteName: "lab", RemoteSession: &sessions[i], Level: 1,
		})
	}
	home.cursor = 1
	key := remotePreviewCacheKey("lab", sessions[0].ID)
	home.previewCacheMu.Lock()
	home.previewCache[key] = devanagariLastResponse + "\n" + tag
	home.previewCacheMu.Unlock()
	return home
}

// Golden frame: the Devanagari last response next to the session list at
// 200x50, and no row of it wider than the pane under any convention.
func TestDevanagariPreviewFrame_Golden_Issue2334(t *testing.T) {
	frame := devanagariPreviewHome(t, "running", "tag-A").View()
	if got := strings.Count(frame, "\n") + 1; got != 50 {
		t.Fatalf("frame has %d rows, want 50", got)
	}
	assertNoOverwideRows(t, frame, 200)
	assertFrameGolden(t, "devanagari_preview_200x50.golden", stripAnsi(frame))
}

// The #2334 mechanism, end to end: two consecutive frames of the real Home
// (status running -> waiting, as in the screenshot's two header blocks) go
// through Bubble Tea's real line-diff renderer into a terminal model of each
// width convention. The screen must equal the frame, row for row. Before the
// fix, on the per-code-point and Ghostty 1.3 terminals each over-wide Devanagari row wrapped
// onto the next screen row (its tail landing in the session-list column) and
// every later row was one off from what the renderer believed, so the
// renderer's partial repaint left the old header block and body lines on
// screen next to the new ones. Auto-wrap must be on after every single write,
// so no exit or kill between writes can leak it off.
func TestDevanagariPreview_NoWrapDesyncAcrossFrames_Issue2334(t *testing.T) {
	frames := []string{
		devanagariPreviewHome(t, "running", "tag-A").View(),
		devanagariPreviewHome(t, "waiting", "tag-B").View(),
	}
	chunks, onScreen := renderThroughBubbleTea(t, frames, []string{"tag-A", "tag-B"}, 200, 50)
	want := strings.Split(stripAnsi(frames[1]), "\n")
	for i := range want {
		want[i] = strings.TrimRight(want[i], " ")
	}
	for _, conv := range widthConventions {
		t.Run(conv.name, func(t *testing.T) {
			term := newWrapTerm(200, 50, conv.conv)
			var got []string
			for i, c := range chunks {
				_, _ = term.Write(c)
				if !term.autowrap {
					t.Fatalf("auto-wrap left off after write %d: %q", i, c)
				}
				if i == onScreen-1 {
					got = term.rows()
				}
			}
			if headers := countRowsContaining(got, "Viewers:"); headers != 1 {
				t.Errorf("screen shows %d preview header blocks, want 1", headers)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("screen row %d differs from the frame\n got: %q\nwant: %q", i, got[i], want[i])
				}
			}
			if t.Failed() {
				t.Logf("screen:\n%s", strings.Join(got, "\n"))
			}
		})
	}
}

var sessionNameRE = regexp.MustCompile(`conductor-buddi-sharjeel|Sharjeel-r\d+-bug\d+`)

func countRowsContaining(rows []string, s string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, s) {
			n++
		}
	}
	return n
}

// execProbe is the attach hand-off stand-in: Bubble Tea runs it the way it
// runs attachCmd, and it records the terminal state the attached program
// would inherit.
type execProbe struct {
	chunks   func() [][]byte
	autowrap *bool
}

func (e execProbe) Run() error {
	term := newWrapTerm(80, 24, convCluster)
	for _, c := range e.chunks() {
		_, _ = term.Write(c)
	}
	*e.autowrap = term.autowrap
	return nil
}
func (execProbe) SetStdin(io.Reader)  {}
func (execProbe) SetStdout(io.Writer) {}
func (execProbe) SetStderr(io.Writer) {}

type execFramesModel struct {
	framesModel
	probe execProbe
}

type startExecMsg struct{}

func (m *execFramesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if _, ok := msg.(startExecMsg); ok {
		return m, tea.Exec(m.probe, func(error) tea.Msg { return tea.Quit() })
	}
	_, cmd := m.framesModel.Update(msg)
	return m, cmd
}

// The attach hand-off (tea.Exec, how attachCmd runs tmux attach) and the
// normal exit both leave the outer terminal with auto-wrap on, after a frame
// whose rows were drawn with it off.
func TestAutoWrapRestoredOnAttachAndExit_Issue2334(t *testing.T) {
	frame := clampViewToViewport(complexScriptCorpus[1].text+"\ntag-exec", 40, 4)
	rec := &chunkRecorder{}
	snapshot := func() [][]byte {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return append([][]byte(nil), rec.chunks...)
	}
	var wrapAtExec bool
	m := &execFramesModel{framesModel: framesModel{frames: []string{frame}}, probe: execProbe{chunks: snapshot, autowrap: &wrapAtExec}}
	// A real (empty) input: Bubble Tea's RestoreTerminal after tea.Exec
	// restarts the input reader and panics on a nil one.
	p := tea.NewProgram(m, tea.WithOutput(rec), tea.WithInput(strings.NewReader("")), tea.WithAltScreen(), tea.WithoutSignalHandler())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
	p.Send(tea.WindowSizeMsg{Width: 40, Height: 4})
	rec.waitFor(t, "tag-exec")
	if !rec.contains(ansi.ResetModeAutoWrap) {
		t.Fatal("frame was drawn without auto-wrap off")
	}
	p.Send(startExecMsg{})
	<-done
	if !wrapAtExec {
		t.Fatal("attached program inherited auto-wrap OFF")
	}
	term := newWrapTerm(80, 24, convCluster)
	for _, c := range snapshot() {
		_, _ = term.Write(c)
	}
	if !term.autowrap {
		t.Fatal("auto-wrap left OFF after the program exited")
	}
}

// Class-level guard (#2334, and the duplicated group row seen with no
// complex script on screen): on a terminal that draws East Asian Ambiguous
// glyphs 2 cells wide, every row of the real frame, the exact-fill header
// bar included, is wider than the deck can know. With auto-wrap off per row
// each row clips at the margin, so the frame keeps its structure: the
// titles stay on their row, every list row stays on its row, and the
// running->waiting repaint leaves exactly one preview header behind.
func TestAmbiguousWideTerminal_FrameKeepsItsRows_Issue2334(t *testing.T) {
	frames := []string{
		devanagariPreviewHome(t, "running", "tag-A").View(),
		devanagariPreviewHome(t, "waiting", "tag-B").View(),
	}
	chunks, onScreen := renderThroughBubbleTea(t, frames, []string{"tag-A", "tag-B"}, 200, 50)
	term := newWrapTerm(200, 50, convAmbiguousWide)
	for _, c := range chunks[:onScreen] {
		_, _ = term.Write(c)
	}
	got := term.rows()
	// Every session name must still sit on its own row: a wrapped row
	// would push the ones below it down.
	for i, row := range strings.Split(stripAnsi(frames[1]), "\n") {
		if name := sessionNameRE.FindString(row); name != "" && !strings.Contains(got[i], name) {
			t.Errorf("row %d lost %q (frame shifted): got %q", i, name, got[i])
		}
	}
	if !strings.HasPrefix(got[2], "SESSIONS") {
		t.Errorf("titles row moved: row 2 = %q", got[2])
	}
	if n := countRowsContaining(got, "conductor-buddi-sharjeel  "); n != 1 {
		t.Errorf("screen shows %d preview header rows, want 1", n)
	}
	if t.Failed() {
		t.Logf("screen:\n%s", strings.Join(got, "\n"))
	}
}
