package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// This starts the full TUI, not the separate session-attach command. An outer
// private tmux supplies both a real terminal and a current rendered cell grid.
func TestNativeSSHTUIRegistryLifecycle(t *testing.T) {
	for _, tool := range []string{"ssh", "tmux"} {
		if _, err := exec.LookPath(tool); err != nil {
			nativeSSHMissingTool(t, tool)
		}
	}
	if dir := os.Getenv("NATIVE_SSH_RECEIPT_DIR"); dir != "" {
		t.Setenv("NATIVE_SSH_RECEIPT_DIR", filepath.Join(dir, "full-tui"))
	}
	bin := channelsCLIBinary(t)
	remote, controller, shim := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(path, body string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }
	env := func(home string) []string {
		var out []string
		for _, item := range os.Environ() {
			key, _, _ := strings.Cut(item, "=")
			if key == "HOME" || key == "TERM" || strings.HasPrefix(key, "XDG_") || strings.HasPrefix(key, "TMUX") || strings.HasPrefix(key, "AGENTDECK_") || key == "CLAUDE_CONFIG_DIR" || key == "CODEX_HOME" {
				continue
			}
			out = append(out, item)
		}
		return append(out, "HOME="+home, "TERM=xterm-256color", "AGENTDECK_TELEMETRY=0")
	}
	command := func(home, executable string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, executable, args...)
		cmd.WaitDelay = time.Second
		cmd.Dir, cmd.Env = home, env(home)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	run := func(home, executable string, args ...string) string {
		t.Helper()
		out, err := command(home, executable, args...)
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", executable, args, err, out)
		}
		return out
	}
	inner := fmt.Sprintf("nt-in-%d", time.Now().UnixNano())
	outer := fmt.Sprintf("nt-out-%d", time.Now().UnixNano())
	write(filepath.Join(remote, ".config", "agent-deck", "config.toml"), "[telemetry]\ndisabled = true\n[claude]\nhooks_enabled = false\n[tmux]\nsocket_name = '"+inner+"'\n[profiles.alice.claude]\nconfig_dir = '"+filepath.Join(remote, "claude-alice")+"'\n")
	proxy := startNativeSSH(t, remote, shim)
	cli := func(args ...string) string {
		t.Helper()
		return run(remote, bin, append([]string{"-p", "default"}, args...)...)
	}
	first, second := "Alpha界", "Betaé"
	nonce := fmt.Sprintf("TUI-NONCE-%d", time.Now().UnixNano())
	receiver := filepath.Join(remote, "receiver.sh")
	write(receiver, "#!/bin/sh\nprintf '%s\\n' "+quote(nonce)+"\nexec sleep 600\n")
	cli("add", remote, "--title", first, "--cmd", "shell", "--wrapper", "sh "+quote(receiver), "--account", "alice", "--json")
	cli("add", remote, "--title", second, "--cmd", "shell", "--account", "alice", "--json")
	cli("session", "start", first, "--json")
	t.Cleanup(func() {
		out, err := command(remote, bin, "-p", "default", "session", "stop", first)
		if err != nil {
			t.Errorf("owned session cleanup: %v %s", err, out)
		}
	})
	registry := func() map[string]string {
		t.Helper()
		var rows []map[string]any
		if err := json.Unmarshal([]byte(cli("list", "--json")), &rows); err != nil {
			t.Fatal(err)
		}
		result := make(map[string]string)
		for _, row := range rows {
			id, ok := row["id"].(string)
			if !ok || id == "" || row["account"] != "alice" {
				t.Fatalf("invalid registry identity/account: %v", row)
			}
			result[fmt.Sprint(row["title"])] = fmt.Sprint(row["id"]) + ":" + fmt.Sprint(row["account"])
		}
		if len(result) != 2 {
			t.Fatalf("expected two independently registered sessions: %v", result)
		}
		return result
	}
	before := registry()
	identity := func() string {
		return strings.TrimSpace(run(remote, "tmux", "-L", inner, "list-panes", "-a", "-F", "#{pid}:#{session_id}:#{pane_id}:#{pane_pid}"))
	}
	beforeIdentity := identity()
	wait := func(label string, condition func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if condition() {
				return
			}
			time.Sleep(40 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", label)
	}
	hasNonce := func() bool {
		for _, line := range strings.Split(cli("session", "output", first), "\n") {
			if strings.TrimSpace(ansi.Strip(line)) == nonce {
				return true
			}
		}
		return false
	}
	wait("executed standalone startup nonce", hasNonce)
	tmux := func(args ...string) string {
		t.Helper()
		return run(controller, "tmux", append([]string{"-L", outer}, args...)...)
	}
	tmux("new-session", "-d", "-s", "full-tui", "-x", "140", "-y", "45", "sleep 600")
	t.Cleanup(func() {
		out, err := command(controller, "tmux", "-L", outer, "kill-session", "-t", "full-tui")
		if err != nil {
			t.Errorf("owned terminal cleanup: %v %s", err, out)
		}
	})
	tmux("set-option", "-t", "full-tui", "remain-on-exit", "on")
	launchNumber := 0
	var pidFile string
	start := func() {
		launchNumber++
		pidFile = filepath.Join(remote, fmt.Sprintf("full-tui-%d.pid", launchNumber))
		// Each launch has a new exclusive private PID file. exec preserves the
		// shell PID through env into the exact built full-TUI executable.
		remoteCommand := "umask 077; set -C; printf '%s' \"$$\" > " + quote(pidFile) + " || exit 1; exec env TERM=xterm-256color AGENTDECK_ACCOUNT=alice " + quote(bin) + " -p default"
		sshCommand := quote(filepath.Join(shim, "ssh")) + " -tt test-host " + quote(remoteCommand)
		tmux("respawn-pane", "-k", "-t", "full-tui:0.0", sshCommand)
	}
	readTUIProcess := func() int {
		t.Helper()
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(string(raw))
		if err != nil || pid <= 1 {
			t.Fatalf("invalid private TUI PID: %q %v", raw, err)
		}
		if err := syscall.Kill(pid, 0); err != nil {
			t.Fatalf("source-defined full TUI process not alive: %d %v", pid, err)
		}
		return pid
	}
	grid := func() string { return tmux("capture-pane", "-p", "-t", "full-tui:0.0") }
	retain := func(name string) {
		path := filepath.Join(controller, name)
		write(path, grid())
		nativeRetainFile(t, name, path)
	}
	t.Cleanup(func() {
		if t.Failed() {
			retain("failure-grid.txt")
		}
	})
	start()
	wait("full TUI current session list and account", func() bool {
		g := grid()
		return strings.Contains(g, first) && strings.Contains(g, second) && strings.Contains(g, "alice")
	})
	retain("initial-grid.txt")
	firstTUIProcess := readTUIProcess()
	tmux("send-keys", "-t", "full-tui:0.0", "/")
	tmux("send-keys", "-l", "-t", "full-tui:0.0", "Beta")
	wait("actual TUI search filters rows", func() bool { g := grid(); return strings.Contains(g, second) && !strings.Contains(g, first) })
	retain("search-grid.txt")
	tmux("send-keys", "-t", "full-tui:0.0", "Escape")
	wait("escape restores both rows", func() bool { g := grid(); return strings.Contains(g, first) && strings.Contains(g, second) })
	// Navigate to a non-default row through the real terminal, then wait until
	// the application's periodic save commits it. Unsaved last-keystroke state
	// is not promised after a dropped connection.
	selected := func(g, title string) bool {
		for _, line := range strings.Split(g, "\n") {
			if strings.Contains(line, "▶") && strings.Contains(line, title) {
				return true
			}
		}
		return false
	}
	tmux("send-keys", "-t", "full-tui:0.0", "End", "Up")
	wait("navigation selects running first session", func() bool { return selected(grid(), first) })
	tmux("send-keys", "-t", "full-tui:0.0", "Enter")
	wait("TUI Enter attaches actual running pane", func() bool {
		g := grid()
		if strings.Contains(g, second) || strings.Contains(g, "SESSIONS") {
			return false
		}
		for _, line := range strings.Split(g, "\n") {
			if strings.TrimSpace(line) == nonce {
				return true
			}
		}
		return false
	})
	retain("entered-running-pane.txt")
	if got := identity(); got != beforeIdentity {
		t.Fatalf("Enter attached replacement identity %s", got)
	}
	tmux("send-keys", "-t", "full-tui:0.0", "C-q")
	wait("CtrlQ returns from actual pane to full TUI", func() bool {
		g := grid()
		return strings.Contains(g, "SESSIONS") && strings.Contains(g, first) && strings.Contains(g, second)
	})
	if got := identity(); got != beforeIdentity || !hasNonce() {
		t.Fatal("CtrlQ changed running pane or transcript")
	}
	retain("returned-tui.txt")
	tmux("send-keys", "-t", "full-tui:0.0", "End")
	wait("navigation selects second session", func() bool { return selected(grid(), second) })
	var dbPath string
	if err := filepath.WalkDir(remote, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "state.db" {
			if dbPath != "" {
				return fmt.Errorf("multiple fixture databases")
			}
			dbPath = path
		}
		return nil
	}); err != nil || dbPath == "" {
		t.Fatalf("find fixture registry: %v %q", err, dbPath)
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: dbPath}).String()+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wait("selected session persisted before disconnect", func() bool {
		var raw string
		if err := db.QueryRow("SELECT value FROM metadata WHERE key=?", "ui_state").Scan(&raw); err != nil {
			return false
		}
		var state struct {
			Cursor string `json:"cursor_session_id"`
		}
		if json.Unmarshal([]byte(raw), &state) != nil {
			return false
		}
		return state.Cursor == strings.TrimSuffix(before[second], ":alice")
	})
	oldGrid := grid()
	dividerColumn := func(g string) int {
		for _, line := range strings.Split(g, "\n") {
			if strings.Contains(line, "SESSIONS") && strings.Contains(line, "PREVIEW") {
				before, _, ok := strings.Cut(line, "│")
				if ok {
					return ansi.StringWidth(before)
				}
			}
		}
		return -1
	}
	oldDivider := dividerColumn(oldGrid)
	if oldDivider < 1 {
		t.Fatal("initial application divider absent")
	}
	tmux("resize-window", "-t", "full-tui:0", "-x", "180", "-y", "52")
	wait("full TUI resized rendered layout", func() bool {
		g := grid()
		// Resizing the outer tmux alone cannot extend the application's old
		// 140-cell footer to 180 cells or move its SESSIONS/PREVIEW divider.
		footer := false
		for _, line := range strings.Split(g, "\n") {
			if strings.TrimSpace(line) == strings.Repeat("─", 180) {
				footer = true
			}
		}
		return footer && dividerColumn(g) > oldDivider && g != oldGrid && strings.Contains(g, first) && strings.Contains(g, second) && strings.Contains(g, "alice") && len(strings.Split(strings.TrimSuffix(g, "\n"), "\n")) == 52
	})
	t.Logf("application divider moved from column%d to%d; full-width footer180 cells", oldDivider, dividerColumn(grid()))
	retain("resized-grid.txt")
	assertPreserved := func() {
		t.Helper()
		after := registry()
		if len(after) != len(before) || after[first] != before[first] || after[second] != before[second] {
			t.Fatalf("registry identity/account changed: before=%v after=%v", before, after)
		}
		if got := identity(); got != beforeIdentity {
			t.Fatalf("server/session/pane/PID changed: %s != %s", got, beforeIdentity)
		}
		if !hasNonce() {
			t.Fatal("transcript nonce lost")
		}
	}
	proxy.drop()
	wait("SSH disconnect exits full TUI client", func() bool {
		return strings.TrimSpace(tmux("display-message", "-p", "-t", "full-tui:0.0", "#{pane_dead}")) == "1"
	})
	wait("source-defined server TUI process reaped after SSH loss", func() bool { return errors.Is(syscall.Kill(firstTUIProcess, 0), syscall.ESRCH) })
	assertPreserved()
	start()
	wait("reconnected full TUI current list", func() bool {
		g := grid()
		return strings.Contains(g, first) && strings.Contains(g, second) && strings.Contains(g, "alice")
	})
	retain("reconnected-grid.txt")
	secondTUIProcess := readTUIProcess()
	if firstTUIProcess == secondTUIProcess {
		t.Fatal("reconnect did not create a distinct TUI process")
	}
	wait("persisted selected row restored", func() bool { return selected(grid(), second) })
	assertPreserved()
	tmux("send-keys", "-t", "full-tui:0.0", "q")
	wait("graceful full TUI quit", func() bool {
		return strings.TrimSpace(tmux("display-message", "-p", "-t", "full-tui:0.0", "#{pane_dead}")) == "1"
	})
	wait("source-defined server TUI process reaped after quit", func() bool { return errors.Is(syscall.Kill(secondTUIProcess, 0), syscall.ESRCH) })
	assertPreserved()
	t.Logf("full TUI exec PIDs %d and%d both reached ESRCH after their client lifetime", firstTUIProcess, secondTUIProcess)
	t.Logf("preserved registry=%v tmux=%s nonce=%s", before, beforeIdentity, nonce)
}
