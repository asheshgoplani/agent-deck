package tmux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SetStatusLeftGlobal hands tmux one argv element. It must arrive unchanged
// (the old shell-style quote rewrite made "it's" show with backslashes), and
// a title escaped with FormatLiteral must render literally: tmux must not run
// the "#(...)" job when it expands status-left.
func TestSetStatusLeftGlobal_LiteralTitleOnRealTmux(t *testing.T) {
	skipIfNoTmuxBinary(t)
	SetDefaultSocketName("")
	t.Cleanup(func() {
		_ = exec.Command("tmux", "set-option", "-gu", "status-left").Run()
		_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()
		simulateTUIRestart()
	})
	_ = exec.Command("tmux", "set-option", "-gu", statusLeftOriginalOption).Run()
	const theme = "#[fg=blue] it's mine #S "
	if err := exec.Command("tmux", "set-option", "-g", "status-left", theme).Run(); err != nil {
		t.Fatalf("set theme: %v", err)
	}
	simulateTUIRestart()

	marker := filepath.Join(t.TempDir(), "pwned")
	bar := "⚡ [1] it's " + FormatLiteral("x#(touch "+marker+")")
	if err := SetStatusLeftGlobal(bar); err != nil {
		t.Fatalf("SetStatusLeftGlobal: %v", err)
	}
	if got := globalStatusLeft(t); got != bar {
		t.Fatalf("status-left = %q, want it unchanged %q", got, bar)
	}
	out, err := exec.Command("tmux", "display-message", "-p", "#{E:status-left}").Output()
	if err != nil {
		t.Fatalf("expand status-left: %v", err)
	}
	if want := "it's x#(touch " + marker + ")"; !strings.Contains(string(out), want) {
		t.Fatalf("expanded status-left %q does not show the title literally (%q)", out, want)
	}
	time.Sleep(300 * time.Millisecond) // a #() job would run asynchronously
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("tmux ran the #() job from the title (marker stat err %v)", err)
	}

	// The user's own status-left comes back verbatim, quote included.
	if err := ClearStatusLeftGlobal(); err != nil {
		t.Fatalf("ClearStatusLeftGlobal: %v", err)
	}
	if got := globalStatusLeft(t); got != theme {
		t.Fatalf("restored status-left = %q, want %q", got, theme)
	}
}
