//go:build !windows

package web

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// ghosttyTerminfo stands in for Ghostty's terminfo entry, which most hosts and
// CI images do not ship. The part that matters is the same as the real entry:
// RGB is advertised (Tc) and the RGB sequences use the colon form that the
// vendored xterm.js misparses.
const ghosttyTerminfo = `xterm-ghostty|synthetic ghostty truecolor entry,
	Tc,
	setrgbf=\E[38:2:%p1%d:%p2%d:%p3%dm,
	setrgbb=\E[48:2:%p1%d:%p2%d:%p3%dm,
	use=xterm-256color,
`

var (
	colonRGB = regexp.MustCompile(`\x1b\[[34]8:2:`)
	// Foreground and background of the sample, as xterm.js can parse them:
	// semicolon RGB, or the 256 colour approximation tmux uses when the
	// client is not known to support RGB.
	xtermBG = regexp.MustCompile(`\x1b\[48;(2;24;25;38|5;\d+)m`)
	xtermFG = regexp.MustCompile(`\x1b\[38;(2;202;211;245|5;\d+)m`)
)

// tmuxReadsCOLORTERM reports whether the installed tmux treats the attach
// client's COLORTERM=truecolor as an RGB hint, which tmux does from 3.6 on.
func tmuxReadsCOLORTERM(t *testing.T) bool {
	t.Helper()
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		t.Fatalf("tmux -V: %v", err)
	}
	m := regexp.MustCompile(`(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return true // e.g. "tmux next-3.7": development builds are current
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	return major > 3 || (major == 3 && minor >= 6)
}

func TestTmuxAttachCommand_RGBOutput(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	if _, err := exec.LookPath("tic"); err != nil {
		t.Skip("tic is not installed")
	}
	terminfo := t.TempDir()
	src := filepath.Join(t.TempDir(), "xterm-ghostty.ti")
	if err := os.WriteFile(src, []byte(ghosttyTerminfo), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("tic", "-x", "-o", terminfo, src).CombinedOutput(); err != nil {
		t.Fatalf("compile synthetic xterm-ghostty terminfo: %v: %s", err, out)
	}
	t.Setenv("TERMINFO", terminfo)
	t.Setenv("TERM", "xterm-ghostty")
	t.Setenv("COLORTERM", "")
	socket := fmt.Sprintf("web-rgb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })
	// A private server and synthetic pane keep live sessions and user config
	// out of this reproduction of the Ghostty-launched web daemon.
	output, err := exec.Command("tmux", "-L", socket, "-f", "/dev/null", "new-session", "-d",
		"-s", "colors", "-x", "80", "-y", "24",
		"sh -c 'printf \"\\033[48;2;24;25;38m\\033[38;2;202;211;245mRGB sample\"; sleep 30'").CombinedOutput()
	if err != nil {
		t.Fatalf("create private RGB pane: %v: %s", err, output)
	}
	cmd := tmuxAttachCommand("colors", socket)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	// tmux writes the sample's attributes before its text, so once the text
	// is on the wire the colours it was drawn with are too.
	wire := make(chan []byte, 1)
	go func() {
		var data []byte
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			data = append(data, buf[:n]...)
			if bytes.Contains(data, []byte("RGB sample")) || err != nil {
				wire <- data
				return
			}
		}
	}()
	var data []byte
	select {
	case data = <-wire:
	case <-time.After(5 * time.Second):
		t.Fatal("tmux attach did not draw the RGB sample")
	}
	if !bytes.Contains(data, []byte("RGB sample")) {
		t.Fatalf("attach ended before drawing the RGB sample: %q", data)
	}
	if m := colonRGB.Find(data); m != nil {
		t.Fatalf("attach emitted colon-form RGB %q, which xterm.js misreads as other colours", m)
	}
	if !xtermBG.Match(data) || !xtermFG.Match(data) {
		t.Fatalf("attach did not emit xterm-compatible foreground and background: %q", data)
	}
	if tmuxReadsCOLORTERM(t) && (!strings.Contains(string(data), "\x1b[48;2;24;25;38m") ||
		!strings.Contains(string(data), "\x1b[38;2;202;211;245m")) {
		t.Fatalf("tmux 3.6+ should honour COLORTERM=truecolor and keep exact RGB: %q", data)
	}
}
