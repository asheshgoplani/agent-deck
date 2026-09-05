package ui

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
)

// Dashboard chrome can appear and disappear without a WindowSizeMsg: the
// maintenance banner arrives on its own message and clears on a timer, the
// update nudge on its check. Each takes a row from the embedded pane, and the
// child PTY has to follow, or tmux keeps drawing one row too many (clipped
// output) or too few (a blank row, mouse rows off by one) until the user
// resizes the outer window.

func newGeometryTestTerminal(t *testing.T) *embeddedTerminal {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("open pty: %v", err)
	}
	t.Cleanup(func() { _ = ptmx.Close(); _ = tty.Close() })
	emulator := vt.NewSafeEmulator(80, 24)
	t.Cleanup(func() { _ = emulator.Close() })
	return &embeddedTerminal{ptmx: ptmx, emulator: emulator, dirty: make(chan struct{}, 1)}
}

func ptyRows(t *testing.T, terminal *embeddedTerminal) int {
	t.Helper()
	size, err := pty.GetsizeFull(terminal.ptmx)
	if err != nil {
		t.Fatalf("read pty size: %v", err)
	}
	return int(size.Rows)
}

func TestEmbeddedGeometryFollowsDashboardChrome(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.compactSidebar = true
	home.embeddedLayout = true
	home.embeddedMode = true
	home.insertMode = true
	home.embeddedTerminal = newGeometryTestTerminal(t)

	home.syncEmbeddedTerminalGeometry()
	base := home.embeddedPaneRect().Height
	if got := ptyRows(t, home.embeddedTerminal); got != base {
		t.Fatalf("pty rows after install = %d, want pane height %d", got, base)
	}

	// The maintenance banner arrives on its own message, no WindowSizeMsg.
	model, _ := home.Update(maintenanceCompleteMsg{result: session.MaintenanceResult{PrunedLogs: 3}})
	home = model.(*Home)
	if home.maintenanceMsg == "" {
		t.Fatal("fixture did not raise the maintenance banner")
	}
	if got := ptyRows(t, home.embeddedTerminal); got != base-1 {
		t.Fatalf("pty rows with banner = %d, want %d (the banner took a row)", got, base-1)
	}

	model, _ = home.Update(clearMaintenanceMsg{})
	home = model.(*Home)
	if got := ptyRows(t, home.embeddedTerminal); got != base {
		t.Fatalf("pty rows after banner cleared = %d, want %d", got, base)
	}
}

func TestEmbeddedGeometryResizesOnlyWhenTheRectangleMoves(t *testing.T) {
	home := NewHome()
	home.width = 100
	home.height = 30
	home.compactSidebar = true
	home.embeddedLayout = true
	home.embeddedMode = true
	home.insertMode = true
	home.embeddedTerminal = newGeometryTestTerminal(t)

	home.syncEmbeddedTerminalGeometry()
	applied := home.embeddedAppliedRect
	if applied == (terminalCellRect{}) {
		t.Fatal("first sync did not record the applied rectangle")
	}

	// Shrink the PTY behind Home's back; an unchanged rectangle must not
	// re-issue the resize (that is what keeps the refresh-tick backstop free).
	if err := pty.Setsize(home.embeddedTerminal.ptmx, &pty.Winsize{Cols: 10, Rows: 5}); err != nil {
		t.Fatalf("setsize: %v", err)
	}
	home.syncEmbeddedTerminalGeometry()
	if got := ptyRows(t, home.embeddedTerminal); got != 5 {
		t.Fatalf("unchanged rectangle re-sent a resize (rows=%d)", got)
	}

	// Leaving the session forgets the applied rectangle so the next install
	// always sizes its fresh PTY. (The fixture terminal has no child process
	// to close, so detach it before the exit path runs Close.)
	home.embeddedTerminal = nil
	home.exitInsertMode()
	if home.embeddedAppliedRect != (terminalCellRect{}) {
		t.Fatalf("applied rectangle survived exit: %+v", home.embeddedAppliedRect)
	}
}
