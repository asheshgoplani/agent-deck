package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Issue #3105: `agent-deck session attach` blocked about 5 s before starting
// the tmux client when the terminal did not answer the background-colour
// query (OSC 11 ; ? followed by CSI 6 n), and termenv swallowed every key
// typed meanwhile. The query came from bubbletea's package init, which runs
// lipgloss.HasDarkBackground() in every process that links bubbletea, so every
// CLI command paid for it, not only the TUI.

const (
	issue3105OSC11Query = "\x1b]11;?"
	issue3105CursorPos  = "\x1b[6n"
)

// issue3105Env is a sandboxed environment for the real binary on a PTY: a
// temp HOME with a private tmux socket name, a private TMUX_TMPDIR, no TMUX,
// and a TERM for which termenv does issue the query (it skips screen*, tmux*
// and dumb). CI is removed too: termenv treats any non-empty CI as "not a
// TTY" and then never queries (termenv v0.16.0 termenv.go:32), so on a CI
// runner (GitHub Actions sets CI=true) the non-TUI test would pass even
// without the fix and the TUI test would fail even with it.
func issue3105Env(t *testing.T) (env []string, home string) {
	t.Helper()
	home = t.TempDir()
	cfgDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := "[tmux]\nsocket_name = 'issue3105-test'\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	tmuxDir := filepath.Join(home, "tmux-tmp")
	if err := os.MkdirAll(tmuxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, kv := range os.Environ() {
		key := strings.SplitN(kv, "=", 2)[0]
		if key == "HOME" || key == "TERM" || key == "COLORTERM" || key == "COLORFGBG" || key == "CI" ||
			strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "AGENTDECK_") ||
			strings.HasPrefix(key, "AGENT_DECK_") || strings.HasPrefix(key, "TMUX") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"TERM=xterm-256color",
		"TMUX_TMPDIR="+tmuxDir,
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
	)
	return env, home
}

// issue3105Run starts bin with args on a PTY whose terminal never answers a
// query, and collects everything the process writes. stop, when non-nil, is
// checked after each read; returning true ends the run early by killing the
// process (by its own PID). The returned duration is the time to exit or stop.
func issue3105Run(t *testing.T, bin string, args []string, stop func(out []byte) bool) ([]byte, time.Duration, bool) {
	t.Helper()
	env, home := issue3105Env(t)
	cmd := exec.Command(bin, args...)
	cmd.Env = env
	cmd.Dir = home
	start := time.Now()
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 120, Rows: 40})
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu  sync.Mutex
		out []byte
	)
	stopped := make(chan struct{})
	var stopOnce sync.Once
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		buf := make([]byte, 4096)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				mu.Lock()
				out = append(out, buf[:n]...)
				hit := stop != nil && stop(out)
				mu.Unlock()
				if hit {
					stopOnce.Do(func() { close(stopped) })
				}
			}
			if err != nil {
				return
			}
		}
	}()
	waitDone := make(chan struct{})
	go func() { _ = cmd.Wait(); close(waitDone) }()

	early := false
	select {
	case <-waitDone:
	case <-stopped:
		early = true
	case <-time.After(15 * time.Second):
		t.Errorf("agent-deck %v did not exit within 15s", args)
	}
	elapsed := time.Since(start)
	select {
	case <-waitDone:
	default:
		_ = cmd.Process.Kill()
		<-waitDone
	}
	_ = terminal.Close()
	<-readDone
	mu.Lock()
	defer mu.Unlock()
	return append([]byte(nil), out...), elapsed, early
}

// TestIssue3105_NonTUICommandsSendNoTerminalQuery runs non-TUI commands,
// including the `session attach` path from the report, under a PTY that never
// answers. They must not write the OSC 11 or CSI 6 n query (termenv then
// waits up to its 5 s OSCTimeout and eats stdin), and must finish promptly.
func TestIssue3105_NonTUICommandsSendNoTerminalQuery(t *testing.T) {
	bin := channelsCLIBinary(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"version", []string{"version"}},
		{"session_attach", []string{"session", "attach", "no-such-session-3105"}},
		{"list", []string{"list"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, elapsed, _ := issue3105Run(t, bin, tc.args, nil)
			if bytes.Contains(out, []byte(issue3105OSC11Query)) {
				t.Errorf("agent-deck %v wrote the OSC 11 background query to the terminal: %q", tc.args, out)
			}
			if bytes.Contains(out, []byte(issue3105CursorPos)) {
				t.Errorf("agent-deck %v wrote the CSI 6 n cursor query to the terminal: %q", tc.args, out)
			}
			// termenv's OSCTimeout is 5 s; a run that waited on it is far
			// over this bound, a run that never queried is far under it.
			if elapsed > 3*time.Second {
				t.Errorf("agent-deck %v took %v under a terminal that does not answer queries; want under 3s", tc.args, elapsed.Round(time.Millisecond))
			}
		})
	}
}

// TestIssue3105_TUIStillDetectsBackground guards the other side: the TUI
// keeps asking the terminal for its background colour (the bubbles textarea
// uses adaptive colours). The run stops at the first sight of the query, so
// the TUI itself never boots.
func TestIssue3105_TUIStillDetectsBackground(t *testing.T) {
	bin := channelsCLIBinary(t)
	out, _, sawQuery := issue3105Run(t, bin, nil, func(out []byte) bool {
		return bytes.Contains(out, []byte(issue3105OSC11Query))
	})
	if !sawQuery {
		t.Fatalf("bare agent-deck (TUI path) never wrote the OSC 11 background query; output: %q", out)
	}
}
