package tmux

import (
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// simulateTUIRestart drops the in-process capture state, as a new TUI process
// starts with a fresh savedStatusLeft while the tmux server keeps its options.
func simulateTUIRestart() {
	savedStatusLeft.Once = sync.Once{}
	savedStatusLeft.value = ""
	savedStatusLeft.captured = false
}

func globalStatusLeft(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("tmux", "-u", "show-option", "-gv", "status-left").Output()
	if err != nil {
		t.Fatalf("show-option status-left: %v", err)
	}
	return strings.TrimRight(string(out), "\n")
}

// TestIssue2705_StaleBarNotRestoredAfterRestart: a TUI that exits without
// ClearStatusLeftGlobal leaves its notification bar as the global
// status-left. The next TUI must not capture that bar as the user's original
// value and restore it when the waiting list empties (ghost [N] entries whose
// Ctrl+b N keys no longer exist).
func TestIssue2705_StaleBarNotRestoredAfterRestart(t *testing.T) {
	skipIfNoTmuxBinary(t)
	SetDefaultSocketName("")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "set-option", "-gu", "status-left").Run()
		_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()
		simulateTUIRestart()
	})
	_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()

	const theme = "#[fg=blue] my-theme #S "
	if err := exec.Command("tmux", "set-option", "-g", "status-left", theme).Run(); err != nil {
		t.Fatalf("set theme: %v", err)
	}

	// First TUI shows a bar, then is killed without cleanup.
	simulateTUIRestart()
	if err := SetStatusLeftGlobal("⚡ [1] A [2] B"); err != nil {
		t.Fatalf("SetStatusLeftGlobal: %v", err)
	}

	// Second TUI starts while the stale bar is still up, shows its own bar,
	// then the waiting list empties.
	simulateTUIRestart()
	if err := SetStatusLeftGlobal("⚡ [1] C"); err != nil {
		t.Fatalf("SetStatusLeftGlobal: %v", err)
	}
	if err := ClearStatusLeftGlobal(); err != nil {
		t.Fatalf("ClearStatusLeftGlobal: %v", err)
	}
	if got := globalStatusLeft(t); got != theme {
		t.Fatalf("status-left after clear = %q, want the user's theme %q (stale bar restored, #2705)", got, theme)
	}
}

// TestIssue2705_StaleBarWithoutStoredOriginalFallsBackToDefault: with a
// leftover bar and no original saved on the server (a bar left by an older
// agent-deck), clearing must unset status-left rather than restore the bar.
func TestIssue2705_StaleBarWithoutStoredOriginalFallsBackToDefault(t *testing.T) {
	skipIfNoTmuxBinary(t)
	SetDefaultSocketName("")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "set-option", "-gu", "status-left").Run()
		_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()
		simulateTUIRestart()
	})
	_ = exec.Command("tmux", "set-option", "-gu", "status-left").Run()
	tmuxDefault := globalStatusLeft(t)
	_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()

	if err := exec.Command("tmux", "set-option", "-g", "status-left", "⚡ [1] A [2] B").Run(); err != nil {
		t.Fatalf("set stale bar: %v", err)
	}
	simulateTUIRestart()
	if err := SetStatusLeftGlobal("⚡ [1] C"); err != nil {
		t.Fatalf("SetStatusLeftGlobal: %v", err)
	}
	if err := ClearStatusLeftGlobal(); err != nil {
		t.Fatalf("ClearStatusLeftGlobal: %v", err)
	}
	if got := globalStatusLeft(t); got != tmuxDefault {
		t.Fatalf("status-left after clear = %q, want tmux default %q (stale bar restored, #2705)", got, tmuxDefault)
	}
}
