package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// widthSpec is one of the three fixed terminal sizes every screen is
// captured at.
type widthSpec struct {
	name          string
	width, height int
}

var widthSpecs = []widthSpec{
	{"80x24", 80, 24},
	{"120x40", 120, 40},
	{"200x50", 200, 50},
}

// frameCapture is one captured, scrubbed frame ready for golden diffing and
// the contact sheet.
type frameCapture struct {
	step, width string
	raw, scrub  string
	advisory    string // only for entries explicitly listed in advisoryReasons
}

// widthRun drives one full walkthrough of the binary at a fixed terminal
// size, against its own sandbox's freshly seeded store and live sessions.
type widthRun struct {
	s         *suite
	spec      widthSpec
	tmuxName  string
	sd        *seed
	frames    []frameCapture
	updateBin string
}

// runWidthIsolated builds a brand new sandbox (its own HOME, its own
// private tmux server, its own seeded store and live sessions), drives the
// binary through every step in it at the given size, and tears the whole
// sandbox down again -- see the comment on its call site in main.go for why
// this is a full sandbox per width rather than one seed reused three times.
func runWidthIsolated(ctx context.Context, bin, updateBin string, spec widthSpec) ([]frameCapture, error) {
	s := &suite{bin: bin, ctx: ctx}
	defer s.cleanup()

	if err := s.setup(); err != nil {
		return nil, fmt.Errorf("setup %s: %w", spec.name, err)
	}
	sd, err := s.seedStore()
	if err != nil {
		return nil, fmt.Errorf("seed %s: %w", spec.name, err)
	}

	w := &widthRun{
		s:         s,
		spec:      spec,
		tmuxName:  "vc-run-" + spec.name,
		sd:        sd,
		updateBin: updateBin,
	}
	agentDeck := filepath.Join(s.root, "bin", "agent-deck")
	if _, err := s.exec("tmux", "new-session", "-d", "-s", w.tmuxName,
		"-x", strconv.Itoa(spec.width), "-y", strconv.Itoa(spec.height), agentDeck); err != nil {
		return nil, fmt.Errorf("launch %s: %w", spec.name, err)
	}

	for _, step := range visualCheckSteps {
		if err := runStepWithRetry(w, step); err != nil {
			return w.frames, fmt.Errorf("step %q at %s: %w", step.name, spec.name, err)
		}
		if step.name == "01-list" {
			if err := w.probeRedraw(); err != nil {
				return w.frames, fmt.Errorf("redraw probe at %s: %w", spec.name, err)
			}
		}
	}
	return w.frames, nil
}

// probeRedraw repeats the author's rapid onto/off-session navigation, then
// records the immediate, settled and forced-repaint panes for inspection.
// A duplicate waiting/stopped row after the settle is a persistent defect.
func (w *widthRun) probeRedraw() error {
	if err := w.send("Home", "Down", "Down", "Down", "Down"); err != nil {
		return err
	}
	immediate, err := w.pane()
	if err != nil {
		return err
	}
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.paneStyledStable()
		if err != nil {
			return false, err
		}
		on, found := cursorOnRow(pane, visibleRowName("claude-stopped", w.spec.width))
		return found && on, nil
	}, 5*time.Second); err != nil {
		return err
	}
	time.Sleep(600 * time.Millisecond)
	settled, err := w.pane()
	if err != nil {
		return err
	}
	if err := w.hardKick(); err != nil {
		return err
	}
	repainted, err := w.pane()
	if err != nil {
		return err
	}
	_, source, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(source))), "visualcheck-artifacts", "redraw")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	for name, frame := range map[string]string{"immediate": immediate, "settled": settled, "repainted": repainted} {
		path := filepath.Join(dir, w.spec.name+"-"+name+".txt")
		if err := os.WriteFile(path, []byte(frame), 0644); err != nil {
			return err
		}
	}
	for _, title := range []string{"claude-waiting", "claude-stopped"} {
		count := 0
		for _, line := range listBodyLines(settled) {
			if strings.Contains(sessionsColumn(line), visibleRowName(title, w.spec.width)) {
				count++
			}
		}
		if count != 1 {
			return fmt.Errorf("%s has %d settled rows, want one", title, count)
		}
	}
	return nil
}

// runStepWithRetry retries failed navigation or golden comparisons twice.
// Exhaustion fails the width; it never turns a failure into advisory.
func runStepWithRetry(w *widthRun, step visualCheckStep) error {
	attempts := 3
	if step.name == "14-fork" {
		// Fork mutates the store. Retrying would create duplicate sessions.
		attempts = 1
	}
	updating := os.Getenv("UPDATE_GOLDEN") == "1"
	var lastErr error
	startFrames := len(w.frames)
	for attempt := 1; attempt <= attempts; attempt++ {
		w.frames = w.frames[:startFrames] // discard any partial capture from a failed attempt
		lastErr = step.run(w)
		// A step that ran without error but produced a frame that doesn't
		// match its golden is retried exactly like a step that errored:
		// this is the same redraw-race class moveCursorToText's hard-kick
		// already fixes for outright failures (a stale scrollback/preview
		// summary snapshotted mid-update), just discovered a step later,
		// after the content is already in hand instead of a wait timing
		// out. Skipped entirely while regenerating goldens -- there is
		// nothing to compare against that isn't itself.
		if lastErr == nil && !updating {
			lastErr = firstGoldenMismatch(w.frames[startFrames:])
		}
		if lastErr == nil {
			return nil
		}
		if attempt < attempts {
			_ = w.hardKick()
			_ = w.send("Home")
			time.Sleep(150 * time.Millisecond)
		}
	}
	pane, _ := w.pane()
	return fmt.Errorf("failed after %d attempts: %w\nlast pane:\n%s", attempts, lastErr, pane)
}

// firstGoldenMismatch compares each already-captured frame against its
// committed golden and returns an error describing the first mismatch, or
// nil if every frame (including any advisory ones, which carry no golden)
// matches. A missing golden also fails the step.
func firstGoldenMismatch(frames []frameCapture) error {
	for _, f := range frames {
		if f.advisory != "" {
			continue
		}
		st, err := compareGolden(f.step, f.width, f.scrub)
		if err != nil {
			return err
		}
		if st.status != "PASS" {
			return fmt.Errorf("%s/%s golden %s", f.step, f.width, st.status)
		}
	}
	return nil
}

// send sends a tmux key spec (e.g. "Down", "Enter", "Escape", "C-s") to this
// run's window.
func (w *widthRun) send(keys ...string) error {
	args := append([]string{"send-keys", "-t", w.tmuxName}, keys...)
	_, err := w.s.exec("tmux", args...)
	return err
}

func (w *widthRun) pane() (string, error) { return w.s.capturePane(w.tmuxName) }

// paneStyled captures the pane with its SGR escape sequences intact (tmux
// capture-pane -e), used only for locating the cursor row: the selected row
// is conveyed purely by a background color swap, with no plain-text marker
// glyph, so cursor detection needs the styled frame even though every
// step's saved/diffed frame is plain text (w.pane / w.capture).
func (w *widthRun) paneStyled() (string, error) {
	out, err := w.s.exec("tmux", "capture-pane", "-p", "-e", "-t", w.tmuxName)
	if err != nil {
		return "", err
	}
	return out, nil
}

// paneStyledStable is paneStyled with a debounce: it re-captures until two
// consecutive reads agree (or gives up after a few tries and returns the
// last one). tmux capture-pane samples the terminal buffer live, and can
// land mid-repaint while bubbletea's full-screen clear+redraw is still in
// flight, producing a torn frame (e.g. a stale row left behind alongside
// the new one). A single retry loop with matching consecutive reads is
// cheap insurance against that without resorting to a bare sleep.
func (w *widthRun) paneStyledStable() (string, error) {
	prev, err := w.paneStyled()
	if err != nil {
		return "", err
	}
	for i := 0; i < 5; i++ {
		time.Sleep(20 * time.Millisecond)
		cur, err := w.paneStyled()
		if err != nil {
			return "", err
		}
		if cur == prev {
			return cur, nil
		}
		prev = cur
	}
	return prev, nil
}

// waitContains blocks until the window's pane contains substr.
func (w *widthRun) waitContains(substr string, timeout time.Duration) error {
	return w.s.waitForPaneContains(w.tmuxName, substr, timeout)
}

// waitFor blocks until fn returns true or timeout elapses.
func (w *widthRun) waitFor(fn func() (bool, error), timeout time.Duration) error {
	return w.s.waitFor(timeout, fn)
}

// capture records the current pane content as the frame for a step, scrubbed
// and ready for diffing.
func (w *widthRun) capture(step string) {
	raw, err := w.pane()
	if err != nil {
		raw = "<capture failed: " + err.Error() + ">"
	}
	// A frame that shows the SESSIONS list is prone to the cold-load
	// vs. settled-hook-status race stepList's comment describes, on every
	// return to the list, not just the very first one. This is a
	// best-effort extra settle, not a hard gate (capture() has no error to
	// report to callers that don't check one): if it never settles, the
	// frame captured is whatever the pane actually shows, which any
	// resulting DIFF will report honestly.
	if strings.Contains(raw, "SESSIONS") && !strings.Contains(raw, "● 1") {
		_ = w.waitFor(func() (bool, error) {
			pane, err := w.pane()
			if err != nil {
				return false, err
			}
			raw = pane
			return strings.Contains(pane, "● 1"), nil
		}, 5*time.Second)
	}
	w.frames = append(w.frames, frameCapture{
		step:  step,
		width: w.spec.name,
		raw:   raw,
		scrub: scrubFrame(raw),
	})
}

// captureAdvisory records a step that could not be reached deterministically
// headlessly. reason is shown in the contact sheet instead of a PASS/DIFF
// mark, per the PROMPT's "if a step is flaky, mark it advisory and say why".
func (w *widthRun) captureAdvisory(step, reason string) {
	if declared, ok := advisoryReasons[step]; !ok || declared != reason {
		panic("undeclared advisory: " + step)
	}
	w.frames = append(w.frames, frameCapture{step: step, width: w.spec.name, advisory: reason})
}

// Advisory entries require a deliberate, reviewed exclusion and a reason.
// There are currently no exclusions: every named screen is required.
var advisoryReasons = map[string]string{}

// cursorHighlightBG is the background-color SGR substring the TUI paints
// behind the selected row (and nothing else static on the list screen,
// besides the always-selected "All" filter tab); see internal/web's Tokyo
// Night accent. Detected empirically from a live `tmux capture-pane -e`
// against the seeded gallery — there is no plain-text cursor glyph, the
// selection is color only.
const cursorHighlightBG = "48;2;121;162;247"

// galleryRowOrder is the SESSIONS tree's fixed top-to-bottom row order, a
// direct consequence of seed.go's insertion order (groups first, alphabetic
// group nesting, sessions added in a fixed sequence within each group) with
// no MRU/pin sort active in the sandbox config. Deriving offsets from this
// known order and jumping with "Home" + a fixed run of "Down" presses is far
// more robust than a live binary search off the cursor's highlight color:
// sending a long, fast burst of arrow keys while re-capturing between each
// one was observed (empirically, on the g14-equivalent local rehearsal) to
// occasionally catch bubbletea's differential redraw mid-update, leaving a
// stale duplicate row in tmux's captured buffer. Home is the one point every
// run genuinely needs live detection for; every hop after it is arithmetic.
var galleryRowOrder = []string{
	"alpha", "opencode-idle", "claude-idle", "claude-waiting", "claude-stopped",
	"backend", "codex-idle", "claude-running", "claude-error",
	"beta", "gemini-idle", "pi-idle", "claude-i18n", "shell-live",
	"remotes/lab",
}

// moveCursorToText jumps to the top of the list ("Home") and presses Down
// exactly as many times as galleryRowOrder says the wanted row sits below
// it, verifying arrival once at the end rather than after every keystroke.
func (w *widthRun) moveCursorToText(text string, maxPresses int) error {
	offset := -1
	for i, t := range galleryRowOrder {
		if t == text {
			offset = i
			break
		}
	}
	if offset < 0 {
		return fmt.Errorf("moveCursorToText: %q is not in galleryRowOrder", text)
	}
	// Home is resent every attempt, not sent once and passively waited for.
	// Rehearsal against the real binary found a reproducible case where
	// navigating onto a session whose preview was still asynchronously
	// loading, then straight off it again, leaves a torn partial repaint:
	// the SESSIONS column keeps a stale duplicate row that plain Home/Down
	// keypresses never clear, even though the app is provably still live
	// and tracking cursor movement underneath the stale paint (confirmed
	// during rehearsal: further navigation changed the PREVIEW pane
	// correctly while the same two SESSIONS rows stayed corrupted). Only a
	// keypress that forces a genuine full-screen redraw -- opening and
	// closing an overlay -- was observed to clear it. So every few
	// attempts this sends that hard kick (open the help overlay, close it)
	// before trying Home again.
	attempt := 0
	if err := w.waitFor(func() (bool, error) {
		attempt++
		if attempt%3 == 0 {
			if err := w.hardKick(); err != nil {
				return false, err
			}
		}
		if err := w.send("Home"); err != nil {
			return false, err
		}
		time.Sleep(80 * time.Millisecond)
		pane, err := w.paneStyledStable()
		if err != nil {
			return false, err
		}
		on, found := cursorOnRow(pane, galleryRowOrder[0])
		return found && on, nil
	}, 10*time.Second); err != nil {
		return fmt.Errorf("Home never selected the first row: %w", err)
	}
	for i := 0; i < offset; i++ {
		if err := w.send("Down"); err != nil {
			return err
		}
		time.Sleep(40 * time.Millisecond)
	}
	attempt = 0
	return w.waitFor(func() (bool, error) {
		attempt++
		if attempt == 1 {
			// nothing extra: give the last Down's own redraw a chance first
		} else if attempt%3 == 0 {
			if err := w.hardKick(); err != nil {
				return false, err
			}
		} else {
			// Net-zero nudge to force a fresh redraw if this frame is a
			// stale partial repaint, without moving off the target row.
			if err := w.send("Down"); err != nil {
				return false, err
			}
			if err := w.send("Up"); err != nil {
				return false, err
			}
			time.Sleep(80 * time.Millisecond)
		}
		pane, err := w.paneStyledStable()
		if err != nil {
			return false, err
		}
		on, found := cursorOnRow(pane, visibleRowName(text, w.spec.width))
		return found && on, nil
	}, 12*time.Second)
}

func visibleRowName(name string, width int) string {
	// At 80 columns the left pane renders this title as "claude-wait…".
	if width == 80 && name == "claude-waiting" {
		return "claude-wait"
	}
	if width == 80 && name == "claude-stopped" {
		return "claude-stop"
	}
	return name
}

// hardKick forces a genuine full-screen redraw by opening and closing the
// help overlay. See moveCursorToText's Home-loop comment for why this is
// sometimes the only thing that clears a stale partial repaint.
func (w *widthRun) hardKick() error {
	if err := w.send("?"); err != nil {
		return err
	}
	time.Sleep(60 * time.Millisecond)
	if err := w.send("Escape"); err != nil {
		return err
	}
	time.Sleep(60 * time.Millisecond)
	return nil
}

// sessionsColumn returns only the left (SESSIONS tree) half of a rendered
// line, up to the dual-pane divider. Matching against the full line would
// also hit the PREVIEW pane's own text, which for a selected group frequently
// echoes its member titles (e.g. "beta"'s preview lists every session in
// it) -- a false row match on a completely different, non-navigable line.
func sessionsColumn(line string) string {
	if i := strings.IndexRune(line, '│'); i >= 0 { // "│"
		return line[:i]
	}
	return line
}

// listBodyLines returns only the SESSIONS-tree row lines of a captured
// frame: everything strictly between the divider row under "SESSIONS /
// PREVIEW" and the divider row above the footer hint bar. Both the header
// (filter tabs, an occasional one-time tip banner) and the footer key
// legend ("⏎ Attach", "n/N New", ...) render their own little badges with
// the exact same accent background used for the selected row, so scanning
// the whole frame for that color produces false cursor-row matches outside
// the list entirely.
func isSeparatorLine(s string) bool {
	stripped := stripANSI(s)
	// Rune count, not byte length: "─" is 3 bytes, so a byte-length
	// threshold never crosses 50% on a line that's almost entirely that
	// one rune.
	return strings.Count(stripped, "─") > utf8.RuneCountInString(stripped)/2
}

func listBodyLines(styledPane string) []string {
	lines := strings.Split(styledPane, "\n")
	headerIdx, footerIdx := -1, len(lines)
	for i, line := range lines {
		plain := stripANSI(sessionsColumn(line))
		if headerIdx < 0 && strings.Contains(plain, "SESSIONS") {
			headerIdx = i
		}
		// The footer hint bar always opens with the attach/toggle glyph;
		// take the LAST match in case a session title or preview line ever
		// echoes the same rune.
		if strings.Contains(stripANSI(line), "⏎") {
			footerIdx = i
		}
	}
	start := headerIdx + 1
	if headerIdx >= 0 && start < len(lines) && isSeparatorLine(lines[start]) {
		start++ // skip the divider row directly under the SESSIONS/PREVIEW header
	}
	if start < 0 || start >= footerIdx {
		return lines
	}
	body := lines[start:footerIdx]
	out := body[:0:0]
	for _, l := range body {
		if !isSeparatorLine(l) { // a stray divider can still land inside this range
			out = append(out, l)
		}
	}
	return out
}

// cursorOnRow reports (isSelected, found) for the row containing text: found
// is false if text is not visible in this frame at all (still off-screen
// or scrolled away), isSelected is true only when that row also carries the
// cursor's highlight background.
func cursorOnRow(styledPane, text string) (isSelected, found bool) {
	for _, line := range listBodyLines(styledPane) {
		col := sessionsColumn(line)
		if strings.Contains(stripANSI(col), text) {
			return rowSelected(col), true
		}
	}
	return false, false
}

// cursorOnRowText reports whether text appears anywhere in the SESSIONS
// column (left of the dual-pane divider) of any list body row -- unlike
// cursorOnRow, it doesn't care about selection, only presence. Used to
// detect a group's expand/collapse state by whether its children's rows
// are in the list at all, since the PREVIEW column echoes a selected
// group's member titles regardless of the group's own collapse state.
func cursorOnRowText(styledPane, text string) bool {
	for _, line := range listBodyLines(styledPane) {
		if strings.Contains(stripANSI(sessionsColumn(line)), text) {
			return true
		}
	}
	return false
}

// rowSelected reports whether a SESSIONS-column line is the cursor's row.
// A selected group header repaints its whole row with the accent
// background (cursorHighlightBG); a selected session/window row instead
// gets a leading "▶" glyph in the tree gutter, replacing the blank
// indent -- two different renderers for the same cursor concept, so both
// have to be checked.
func rowSelected(col string) bool {
	if strings.Contains(col, cursorHighlightBG) {
		return true
	}
	return strings.Contains(stripANSI(col), "▶")
}

// ansiCSI matches a terminal escape sequence (SGR color/style codes) so
// stripANSI can compare visible text without tripping over color codes
// splitting a search substring.
var ansiCSI = regexp.MustCompile("\x1b\\[[0-9;]*[a-zA-Z]")

func stripANSI(s string) string { return ansiCSI.ReplaceAllString(s, "") }
