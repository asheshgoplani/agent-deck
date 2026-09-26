package ui

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// widthConvention is how a terminal sizes the text it prints.
type widthConvention int

const (
	// convCluster sizes a grapheme cluster by its base, as ansi.StringWidth,
	// Claude Code's Bun.stringWidth and utf8proc tmux do.
	convCluster widthConvention = iota
	// convPerCodepoint advances per code point by wcwidth, as xterm,
	// glibc-built tmux and Ghostty's legacy mode do.
	convPerCodepoint
	// convGhostty13 is Ghostty 1.3's default unicode mode (uucode 0.2.0): a
	// cluster starts at its base's width and becomes 2 cells once a member
	// that is not zero-width-in-grapheme (a spacing vowel sign, a second
	// consonant) joins it; VS16 widens, VS15 narrows.
	convGhostty13
	// convAmbiguousWide is a terminal set to draw East Asian Ambiguous
	// characters (● ○ ◐ ■ × │ ─ … ↓ ↑ ▶ · •) as 2 cells: CJK locales, the
	// iTerm2 "ambiguous characters are double-width" option. No clamp can
	// fit it (box drawing widens too); only auto-wrap off keeps rows in place.
	convAmbiguousWide
)

var ambiguousWide = func() *runewidth.Condition {
	c := runewidth.NewCondition()
	c.EastAsianWidth = true
	return c
}()

// wrapTerm is a deliberately small terminal model for #2334: enough of a VT
// (printing with auto-wrap and scrolling, CR/LF, CUP/CUU/CUD/CUF/CUB, EL, ED,
// DECAWM, alt screen) to replay Bubble Tea's renderer output, with a
// pluggable width convention so the same bytes can be drawn the way each
// kind of terminal would.
type wrapTerm struct {
	w, h     int
	cells    [][]string
	x, y     int
	lastX    int
	lastY    int
	autowrap bool
	conv     widthConvention
}

func newWrapTerm(w, h int, conv widthConvention) *wrapTerm {
	t := &wrapTerm{w: w, h: h, autowrap: true, conv: conv}
	t.clear()
	return t
}

func (t *wrapTerm) clear() {
	t.cells = make([][]string, t.h)
	for i := range t.cells {
		t.cells[i] = t.blankRow()
	}
}

func (t *wrapTerm) blankRow() []string {
	r := make([]string, t.w)
	for i := range r {
		r[i] = " "
	}
	return r
}

func (t *wrapTerm) lineFeed() {
	if t.x >= t.w {
		t.x = t.w - 1 // LF clears the pending-wrap state
	}
	if t.y == t.h-1 {
		t.cells = append(t.cells[1:], t.blankRow())
		return
	}
	t.y++
}

// put draws one glyph of cw cells, wrapping or clipping at the right margin
// the way DECAWM on/off does.
func (t *wrapTerm) put(g string, cw int) {
	if cw == 0 {
		t.cells[t.lastY][t.lastX] += g
		return
	}
	if t.x+cw > t.w {
		if t.autowrap {
			t.x = 0
			t.lineFeed()
		} else {
			t.x = t.w - cw
		}
	}
	t.cells[t.y][t.x] = g
	for i := 1; i < cw; i++ {
		t.cells[t.y][t.x+i] = ""
	}
	t.lastX, t.lastY = t.x, t.y
	t.x += cw
}

// text draws printable text under the terminal's width convention.
func (t *wrapTerm) text(s string) {
	for s != "" {
		cluster, w := ansi.FirstGraphemeCluster(s, ansi.GraphemeWidth)
		s = s[len(cluster):]
		switch t.conv {
		case convCluster:
			t.put(cluster, w)
		case convPerCodepoint:
			for _, r := range cluster {
				t.put(string(r), ansi.StringWidthWc(string(r)))
			}
		case convGhostty13:
			t.put(cluster, ghostty13ClusterWidth(cluster))
		case convAmbiguousWide:
			t.put(cluster, max(w, ambiguousWide.StringWidth(cluster)))
		}
	}
}

func (t *wrapTerm) Write(p []byte) (int, error) {
	s := string(p)
	for s != "" {
		switch c := s[0]; {
		case c == '\r':
			t.x, s = 0, s[1:]
		case c == '\n':
			t.lineFeed()
			s = s[1:]
		case c == 0x1b:
			n := escapeSequenceLen(s)
			t.escape(s[:n])
			s = s[n:]
		case c < 0x20:
			s = s[1:]
		default:
			end := strings.IndexAny(s, "\r\n\x1b")
			if end < 0 {
				end = len(s)
			}
			t.text(s[:end])
			s = s[end:]
		}
	}
	return len(p), nil
}

func (t *wrapTerm) escape(seq string) {
	if len(seq) < 3 || seq[1] != '[' {
		return
	}
	final := seq[len(seq)-1]
	params := seq[2 : len(seq)-1]
	if strings.HasPrefix(params, "?") {
		on := final == 'h'
		for _, p := range strings.Split(params[1:], ";") {
			switch p {
			case "7":
				t.autowrap = on
			case "1049":
				// Entering clears; leaving keeps the last frame readable
				// (a real terminal restores the main screen instead).
				if on {
					t.clear()
					t.x, t.y = 0, 0
				}
			}
		}
		return
	}
	nums := strings.Split(params, ";")
	arg := func(i, def int) int {
		if i < len(nums) {
			if v, err := strconv.Atoi(nums[i]); err == nil && v > 0 {
				return v
			}
		}
		return def
	}
	switch final {
	case 'H', 'f':
		t.y, t.x = min(arg(0, 1), t.h)-1, min(arg(1, 1), t.w)-1
	case 'A':
		t.y = max(0, t.y-arg(0, 1))
	case 'B':
		t.y = min(t.h-1, t.y+arg(0, 1))
	case 'C':
		t.x = min(t.w-1, t.x+arg(0, 1))
	case 'D':
		t.x = max(0, min(t.x, t.w-1)-arg(0, 1))
	case 'K':
		for i := min(t.x, t.w-1); i < t.w; i++ {
			t.cells[t.y][i] = " "
		}
	case 'J':
		from := t.y
		if arg(0, 0) == 2 {
			from = 0
		} else {
			for i := min(t.x, t.w-1); i < t.w; i++ {
				t.cells[t.y][i] = " "
			}
			from++
		}
		for r := from; r < t.h; r++ {
			t.cells[r] = t.blankRow()
		}
	}
}

func ghostty13ClusterWidth(cluster string) int {
	w := -1
	for _, r := range cluster {
		rw := ansi.StringWidthWc(string(r))
		switch {
		case w < 0:
			w = rw
		case r == 0xFE0F:
			w = 2
		case r == 0xFE0E:
			w = 1
		case rw == 0, unicode.In(r, unicode.Mn, unicode.Me),
			r >= 0x1F3FB && r <= 0x1F3FF,                           // emoji modifiers
			r >= 0x1160 && r <= 0x11FF, r >= 0xD7B0 && r <= 0xD7FF: // Hangul V/T jamo
		default:
			w = 2
		}
	}
	return max(w, 0)
}

// rows returns the screen as text, trailing blanks trimmed.
func (t *wrapTerm) rows() []string {
	out := make([]string, t.h)
	for i, r := range t.cells {
		out[i] = strings.TrimRight(strings.Join(r, ""), " ")
	}
	return out
}

// widthConventions are the terminal width models #2334 has to survive.
var widthConventions = []struct {
	name string
	conv widthConvention
}{
	{"grapheme-cluster", convCluster},
	{"per-code-point", convPerCodepoint},
	{"ghostty-1.3-unicode", convGhostty13},
}

// assertNoOverwideRows fails when any row of frame would take more than width
// cells on a terminal of any width convention: the #2334 guarantee that
// no row the deck emits can auto-wrap. width <= 0 uses the widest row as the
// app measures it, which is the pane width for a clamped frame.
func assertNoOverwideRows(t *testing.T, frame string, width int) {
	t.Helper()
	rows := strings.Split(strings.TrimRight(frame, "\n"), "\n")
	if width <= 0 {
		for _, row := range rows {
			width = max(width, cellWidth(row))
		}
	}
	for i, row := range rows {
		for _, conv := range widthConventions {
			term := newWrapTerm(width+64, 1, conv.conv)
			term.autowrap = false
			_, _ = term.Write([]byte(ansi.Strip(row)))
			if term.x > width {
				t.Errorf("row %d is %d cells on a %s terminal, pane is %d: %q", i, term.x, conv.name, width, ansi.Strip(row))
			}
		}
	}
}

// chunkRecorder keeps each Write Bubble Tea's renderer makes, in order.
type chunkRecorder struct {
	mu     sync.Mutex
	chunks [][]byte
	all    strings.Builder
}

func (r *chunkRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.chunks = append(r.chunks, append([]byte(nil), p...))
	r.all.Write(p)
	return len(p), nil
}

// contains reports whether s was written, as visible text or as a raw
// escape sequence.
func (r *chunkRecorder) contains(s string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	all := r.all.String()
	return strings.Contains(all, s) || strings.Contains(ansi.Strip(all), s)
}

func (r *chunkRecorder) waitFor(t *testing.T, s string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !r.contains(s) {
		if time.Now().After(deadline) {
			t.Fatalf("%q was never written", s)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

type framesModel struct {
	frames []string
	i      int
}

type showFrameMsg int

func (m *framesModel) Init() tea.Cmd { return nil }
func (m *framesModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if f, ok := msg.(showFrameMsg); ok {
		m.i = int(f)
	}
	return m, nil
}
func (m *framesModel) View() string { return m.frames[m.i] }

// renderThroughBubbleTea plays frames through Bubble Tea's real alt-screen
// renderer (line diffing included) and returns every write it made, shutdown
// included, plus how many of them had been made when the last frame was on
// screen (shutdown erases the cursor row, so a screen check replays only
// those). marker[i] is plain text only frame i contains, used to wait for its
// flush.
func renderThroughBubbleTea(t *testing.T, frames, markers []string, width, height int) (chunks [][]byte, onScreen int) {
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
		// Let the renderer finish the tick that carried the marker.
		time.Sleep(40 * time.Millisecond)
	}
	rec.mu.Lock()
	onScreen = len(rec.chunks)
	rec.mu.Unlock()
	p.Quit()
	<-done
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.chunks, onScreen
}
