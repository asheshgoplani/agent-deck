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

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestEval_EmbeddedLocalAndWindowUTF8UnderCLocale(t *testing.T) {
	setIsolatedAgentDeckDir(t)
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
	inst := session.NewInstanceWithTool("unicode", t.TempDir(), "claude")
	inst.GetTmuxSession().Name = "unicode"
	inst.GetTmuxSession().SocketName = socket
	home := &Home{}
	for _, tc := range []struct {
		name   string
		target insertTargetRef
	}{
		{"unicode", insertTargetRef{local: inst}},
		{"unicode:1", insertTargetRef{local: inst, hasWindow: true, windowIndex: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Exercise the production request builder. Raw AttachRequest values
			// intentionally leave ForceUTF8 unset for external terminal launchers.
			req, ok := home.embeddedAttachRequest(tc.target)
			if !ok || req.Name != tc.name || req.SocketName != socket || !req.ForceUTF8 {
				t.Fatalf("embedded request = %+v, ok=%v", req, ok)
			}
			terminal, err := startEmbeddedTerminalWithClipboard(context.Background(), req, embeddedTerminalSize{Cols: 60, Rows: 8}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			eventually(t, 3*time.Second, func() bool { return strings.Contains(terminal.Render(), want) }, "non-UTF-8 locale corrupted embedded Unicode output")
		})
	}
}
