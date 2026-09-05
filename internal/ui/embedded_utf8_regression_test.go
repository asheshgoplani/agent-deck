//go:build eval_smoke

package ui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	deckterminal "github.com/asheshgoplani/agent-deck/internal/terminal"
)

func TestEval_EmbeddedLocalAndWindowUTF8UnderCLocale(t *testing.T) {
	t.Setenv("LC_ALL", "C")
	t.Setenv("LANG", "C")
	t.Setenv("LC_CTYPE", "C")
	socket := fmt.Sprintf("ad-utf8-review-%d", os.Getpid())
	tm := func(args ...string) {
		t.Helper()
		out, err := exec.Command("tmux", append([]string{"-u", "-L", socket}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("tmux: %v: %s", err, out)
		}
	}
	// This private server lives only in the disposable test container.
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	want := "UTF8-λ-日本-🦊"
	command := "printf '%s\\n' " + shellSingleQuote(want) + "; sleep 300"
	tm("new-session", "-d", "-x", "60", "-y", "8", "-s", "unicode", command)
	tm("new-window", "-d", "-t", "unicode:1", command)
	for _, target := range []string{"unicode:0", "unicode:1"} {
		t.Run(target, func(t *testing.T) {
			terminal, err := startEmbeddedTerminalWithClipboard(context.Background(), deckterminal.AttachRequest{Name: target, SocketName: socket}, embeddedTerminalSize{Cols: 60, Rows: 8}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			eventually(t, 3*time.Second, func() bool { return strings.Contains(terminal.Render(), want) }, "non-UTF-8 locale corrupted embedded Unicode output")
		})
	}
}
