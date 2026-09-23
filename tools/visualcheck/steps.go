package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var debugNav = os.Getenv("VISUALCHECK_DEBUG_NAV") == "1"

// visualCheckStep is one entry in the fixed key script every width is
// driven through. run does the key presses, explicit waits, and calls
// w.capture (or w.captureAdvisory) once per screen the step produces.
type visualCheckStep struct {
	name string
	run  func(w *widthRun) error
}

// visualCheckSteps is the PROMPT's key script: list, preview, group view,
// expand/collapse, create dialog, edit, fork, ctrl+s switcher, MCP manager,
// settings, help, update banner, attach and detach of a shell
// session. Order matters only in that "fork" (the one step that leaves a
// lasting mutation) runs last, after every other step's frame has already
// been captured.
var visualCheckSteps = []visualCheckStep{
	{"01-list", stepList},
	{"02-preview", stepPreview},
	{"03-group-view", stepGroupView},
	{"04-expand-collapse", stepExpandCollapse},
	{"05-create-dialog", stepCreateDialog},
	{"06-edit", stepEdit},
	{"07-switcher", stepSwitcher},
	{"08-mcp-manager", stepMCPManager},
	{"09-settings", stepSettings},
	{"10-help", stepHelp},
	{"11-update-banner", stepUpdateBanner},
	{"12-attach-shell", stepAttachShell},
	{"13-detach-shell", stepDetachShell},
	{"14-fork", stepFork},
}

var expectedFrames = []string{
	"01-list", "02-preview", "03-group-view", "04-collapsed", "04-expanded",
	"05-create-dialog", "06-edit", "07-switcher", "08-mcp-manager", "09-settings",
	"10-help", "11-update-banner", "12-attach-shell", "13-detach-shell", "14-fork",
}

func stepList(w *widthRun) error {
	if err := w.waitContains("alpha", 10*time.Second); err != nil {
		return err
	}
	// A freshly launched process's cold status load can briefly show every
	// hook-driven session (claude-waiting/-running/-error) as a generic
	// idle/starting default before it finishes reading their live tmux
	// panes and hook files -- seedStore already confirmed those statuses
	// through the CLI before this TUI ever started, so waiting here for
	// the header's own running-count glyph to appear is waiting for THIS
	// process's status computation to catch up to that already-true
	// reality, not for anything to change. Every later step depends on
	// this: capturing before it settles bakes a wrong status into every
	// subsequent screen that shows this session, not just this one.
	if err := w.waitContains("● 1", 10*time.Second); err != nil {
		return fmt.Errorf("session statuses never settled past cold-load defaults: %w", err)
	}
	w.capture("01-list")
	return nil
}

func stepPreview(w *widthRun) error {
	if err := w.moveCursorToText("claude-i18n", 40); err != nil {
		return err
	}
	if err := w.waitContains(visualCheckI18NReply, 5*time.Second); err != nil {
		return err
	}
	w.capture("02-preview")
	return nil
}

// stepGroupView captures the group-scoped view (`agent-deck --group alpha`):
// Home.SetGroupScope is a launch-time flag, not something a keypress inside
// a normal session reaches -- Enter on a group header only toggles its
// expand/collapse state (see internal/ui/home.go's "enter" case), it does
// not "zoom into" the group. So this launches its own short-lived tmux
// window with that flag alongside the main width-run window, captures it,
// and tears it down, rather than trying to reach scope from inside the
// already-running list.
func stepGroupView(w *widthRun) error {
	name := "vc-groupview-" + w.spec.name
	agentDeck := filepath.Join(w.s.root, "bin", "agent-deck")
	if _, err := w.s.exec("tmux", "new-session", "-d", "-s", name,
		"-x", strconv.Itoa(w.spec.width), "-y", strconv.Itoa(w.spec.height),
		agentDeck, "--group", "alpha"); err != nil {
		return fmt.Errorf("launch group-scoped view: %w", err)
	}
	if os.Getenv("VISUALCHECK_KEEP_SANDBOX") != "1" {
		defer w.s.exec("tmux", "kill-session", "-t", name)
	}

	if err := w.s.waitForPaneContains(name, "alpha", 8*time.Second); err != nil {
		return err
	}
	// The scope hides every other root group's rows.
	if err := w.s.waitFor(5*time.Second, func() (bool, error) {
		pane, err := w.s.capturePane(name)
		if err != nil {
			return false, err
		}
		return !contains(pane, "beta"), nil
	}); err != nil {
		return err
	}
	var raw string
	if err := w.s.waitFor(5*time.Second, func() (bool, error) {
		var err error
		raw, err = w.s.capturePane(name)
		return err == nil && !strings.Contains(raw, "⟳ Reloading..."), err
	}); err != nil {
		return fmt.Errorf("group view did not settle: %w", err)
	}
	w.frames = append(w.frames, frameCapture{step: "03-group-view", width: w.spec.name, raw: raw, scrub: scrubFrame(raw)})
	return nil
}

func stepExpandCollapse(w *widthRun) error {
	if err := w.moveCursorToText("alpha", 40); err != nil {
		return err
	}
	if err := w.send("Tab"); err != nil { // collapse
		return err
	}
	// The SESSIONS column only, not the whole pane: alpha's PREVIEW summary
	// lists its member titles (including claude-idle) regardless of whether
	// the group is collapsed in the left column, so checking the whole pane
	// for that text never sees it disappear.
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.paneStyled()
		if err != nil {
			return false, err
		}
		return !cursorOnRowText(pane, "claude-idle"), nil
	}, 5*time.Second); err != nil {
		return err
	}
	w.capture("04-collapsed")
	// Sent once, not resent on every poll: the expand/collapse has a brief
	// animation, and a second Tab landing mid-animation toggles it right
	// back rather than confirming it.
	if err := w.send("Tab"); err != nil { // expand again
		return err
	}
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.paneStyled()
		if err != nil {
			return false, err
		}
		return cursorOnRowText(pane, "claude-idle"), nil
	}, 5*time.Second); err != nil {
		return err
	}
	w.capture("04-expanded")
	return nil
}

func stepCreateDialog(w *widthRun) error {
	if err := w.send("n"); err != nil {
		return err
	}
	if err := waitScreen(w, 5*time.Second, "New Session", "Name:"); err != nil {
		return err
	}
	w.capture("05-create-dialog")
	// Never submit: leaving the store untouched keeps every later step's
	// list content identical to what "01-list" already proved.
	return closeScreen(w, "New Session")
}

func stepEdit(w *widthRun) error {
	if err := w.moveCursorToText("claude-waiting", 40); err != nil {
		return err
	}
	if err := w.send("P"); err != nil {
		return err
	}
	markers := []string{"Edit Session", "Title:"}
	if w.spec.width == 80 {
		// This dialog is taller than 24 rows; its heading scrolls out of view.
		markers = []string{"same-harness resume", "Title:", "Extra args"}
	}
	if err := waitScreen(w, 5*time.Second, markers...); err != nil {
		return err
	}
	w.capture("06-edit")
	if w.spec.width == 80 {
		return closeScreen(w, "same-harness resume")
	}
	return closeScreen(w, "Edit Session")
}

func stepSwitcher(w *widthRun) error {
	if err := w.send("C-s"); err != nil {
		return err
	}
	if err := waitScreen(w, 5*time.Second, "Switch session", "Ctrl+S next"); err != nil {
		return err
	}
	w.capture("07-switcher")
	return closeScreen(w, "Switch session")
}

func stepMCPManager(w *widthRun) error {
	if err := w.moveCursorToText("claude-waiting", 40); err != nil {
		return err
	}
	if err := w.send("m"); err != nil {
		return err
	}
	if err := waitScreen(w, 5*time.Second, "MCP Manager", "No MCPs configured"); err != nil {
		return err
	}
	w.capture("08-mcp-manager")
	return closeScreen(w, "MCP Manager")
}

func stepSettings(w *widthRun) error {
	if _, err := w.s.exec("tmux", "send-keys", "-l", "-t", w.tmuxName, "S"); err != nil {
		return err
	}
	if err := waitScreen(w, 5*time.Second, "Settings", "THEME", "DEFAULT TOOL"); err != nil {
		return err
	}
	w.capture("09-settings")
	return closeScreen(w, "THEME")
}

func stepHelp(w *widthRun) error {
	if err := w.send("?"); err != nil {
		return err
	}
	if err := waitScreen(w, 5*time.Second, "KEYBOARD SHORTCUTS"); err != nil {
		return err
	}
	w.capture("10-help")
	return closeScreen(w, "KEYBOARD SHORTCUTS")
}

func waitScreen(w *widthRun, timeout time.Duration, markers ...string) error {
	var previous string
	return w.waitFor(func() (bool, error) {
		pane, err := w.pane()
		if err != nil {
			return false, err
		}
		ready := !strings.Contains(pane, "checking...")
		for _, marker := range markers {
			ready = ready && strings.Contains(pane, marker)
		}
		if !ready {
			previous = ""
			return false, nil
		}
		current := scrubFrame(pane)
		stable := previous == current
		previous = current
		return stable, nil
	}, timeout)
}

func closeScreen(w *widthRun, title string) error {
	if err := w.send("Escape"); err != nil {
		return err
	}
	consecutive := 0
	return w.waitFor(func() (bool, error) {
		pane, err := w.pane()
		if err != nil {
			return false, err
		}
		if strings.Contains(pane, title) || !strings.Contains(pane, "SESSIONS") {
			consecutive = 0
			return false, nil
		}
		consecutive++
		return consecutive >= 2, nil
	}, 5*time.Second)
}

func stepUpdateBanner(w *widthRun) error {
	installed := filepath.Join(w.s.root, "bin", "agent-deck")
	staged := installed + ".new"
	if err := copyExecutable(w.updateBin, staged); err != nil {
		return err
	}
	if err := os.Rename(staged, installed); err != nil {
		return err
	}
	if err := w.waitContains("installed, press ctrl+t to restart agent-deck", 15*time.Second); err != nil {
		return err
	}
	w.capture("11-update-banner")
	if err := w.send("C-t"); err != nil {
		return err
	}
	if err := w.waitContains("99.0.0", 15*time.Second); err != nil {
		return fmt.Errorf("restart into installed build: %w", err)
	}
	return nil
}

func stepAttachShell(w *widthRun) error {
	if err := w.moveCursorToText("shell-live", 40); err != nil {
		return err
	}
	if err := w.send("Enter"); err != nil {
		return err
	}
	// Attaching replaces the list chrome with the raw child pane; the
	// sandbox's /bin/sh prompt is the signal it landed.
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.pane()
		if err != nil {
			return false, err
		}
		return !contains(pane, "SESSIONS"), nil
	}, 8*time.Second); err != nil {
		return err
	}
	// The shell's own launch line (with a real per-run instance id and
	// identity-file path embedded in it) can still be scrolled into view
	// and line-wrapped at that point; wrapping around a variable-length id
	// shifts every character after it, which no regex can undo after the
	// fact. `clear` gives a frame that is just the prompt on an empty
	// screen, independent of exactly what scrolled by getting there.
	if _, err := w.s.exec("tmux", "send-keys", "-t", w.tmuxName, "-l", "--", "clear"); err != nil {
		return err
	}
	if err := w.send("Enter"); err != nil {
		return err
	}
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.s.capturePane(w.sd.shellLive.tmuxName)
		return err == nil && strings.Contains(pane, "$") &&
			!strings.Contains(pane, "export AGENTDECK"), err
	}, 5*time.Second); err != nil {
		return err
	}
	// The list preview reads tmux scrollback after detaching. Remove the
	// shell's launch command so its variable-length identity path cannot
	// leak back into that frame.
	if _, err := w.s.exec("tmux", "clear-history", "-t", w.sd.shellLive.tmuxName); err != nil {
		return err
	}
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.pane()
		return err == nil && strings.Contains(pane, "0:sh*") && !strings.Contains(pane, "SESSIONS"), err
	}, 5*time.Second); err != nil {
		return err
	}
	w.capture("12-attach-shell")
	return nil
}

func stepDetachShell(w *widthRun) error {
	if err := w.send("C-q"); err != nil {
		return err
	}
	if err := w.waitContains("SESSIONS", 8*time.Second); err != nil {
		return err
	}
	// Returning to the list re-triggers the same cold-load-vs-settled-status
	// race stepList's comment describes: wait for it here too before capturing.
	if err := w.waitContains("● 1", 8*time.Second); err != nil {
		return fmt.Errorf("session statuses never resettled after detach: %w", err)
	}
	w.capture("13-detach-shell")
	return nil
}

func stepFork(w *widthRun) error {
	if err := w.moveCursorToText("claude-waiting", 40); err != nil {
		return err
	}
	if err := w.send("f"); err != nil {
		return err
	}
	if err := w.waitContains("claude-waiting (fork)", 15*time.Second); err != nil {
		return err
	}
	// A Claude fork in a git project creates a real worktree; the preview
	// pane's "Status:" line starts as "checking..." while an async `git`
	// status call is in flight and only then resolves to clean/dirty. The
	// cursor lands on the new fork row automatically, so that transient
	// text is exactly what a same-instant capture would show.
	polls := 0
	if err := w.waitFor(func() (bool, error) {
		polls++
		if polls%10 == 0 {
			// Re-select the fork so the preview's async git status is refreshed.
			if err := w.send("Down", "Up"); err != nil {
				return false, err
			}
		}
		pane, err := w.pane()
		if err != nil {
			return false, err
		}
		return !contains(pane, "checking..."), nil
	}, 30*time.Second); err != nil {
		return err
	}
	// The fork exists before its tmux client finishes launching. Capture the
	// fixture's settled pane, not an arbitrary spinner frame.
	if err := w.waitFor(func() (bool, error) {
		pane, err := w.pane()
		if err != nil {
			return false, err
		}
		if w.spec.width == 80 {
			// The 24-line preview has no room to show the client's output.
			return strings.Contains(pane, "claude-waiting (fork)  ◐ waiting") &&
				strings.Contains(pane, "Status:  clean"), nil
		}
		return strings.Contains(pane, "Claude Code synthetic fixture") &&
			!strings.Contains(pane, "Starting Claude session..."), nil
	}, 30*time.Second); err != nil {
		return fmt.Errorf("forked client did not settle: %w", err)
	}
	w.capture("14-fork")
	return nil
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
