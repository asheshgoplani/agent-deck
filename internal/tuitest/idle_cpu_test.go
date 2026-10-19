package tuitest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

const (
	idleFleetSessions = 10
	idleFleetRemotes  = 4
	// Hook artifacts of other registered sessions in the shared hooks dir. A
	// long-lived install keeps several files per session it ever ran,
	// archived ones included.
	idleFleetHookSessions = 1000
	idleCPUWarmup         = 15 * time.Second
	idleCPUWindow         = 30 * time.Second
	idleCPUBudgetPercent  = 5.0
	// tuiExitDeadline bounds the wait for the TUI to exit after its tmux
	// server is killed.
	tuiExitDeadline = 20 * time.Second
)

// fakeClaude draws Claude Code's idle screen (banner, done line, composer
// between dividers, permissions footer) and then waits like Claude between
// turns.
const fakeClaude = `#!/bin/sh
printf '\033[2J\033[H'
printf '╭───────────────────────────────╮\r\n│ ✻ Welcome to Claude Code!     │\r\n╰───────────────────────────────╯\r\n\r\n'
printf '> summarize the repo\r\n\r\n⏺ All four steps ran.\r\n\r\n✻ Sautéed for 45s\r\n\r\n'
printf '────────────────────────────────────────\r\n❯ \r\n────────────────────────────────────────\r\n'
printf '  ⏵⏵ bypass permissions on (shift+tab to cycle)'
while :; do sleep 3600; done
`

// fakeSSH answers a remote's session listing with an empty fleet and keeps a
// persistent remote-agent channel open the way a live remote does.
const fakeSSH = `#!/bin/sh
case "$*" in
*remote-agent*) exec cat >/dev/null ;;
*list*) printf '[]\n' ;;
esac
exit 0
`

// TestIdleTUICPUBudget runs the real TUI over a fake fleet (Claude-like
// sessions, ssh remotes, a populated hooks dir) with nobody typing, and
// holds its process CPU time under a budget. It builds with the fswatchpoll
// tag so Linux runs the polling file watcher macOS uses: native fsnotify is
// free when idle, the macOS poller is where idle cost hid (A7).
func TestIdleTUICPUBudget(t *testing.T) {
	testutil.SkipIfShort(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("process CPU time is read from /proc/<pid>/stat, which this OS does not provide")
	}

	binary := buildBinary(t, "fswatchpoll")
	home := t.TempDir()
	// tuiPID is the TUI's pid once it runs. The TUI writes under home (health
	// samples in logs/health, the session event journal) until it exits, so
	// home must not be removed while it lives; this guard, registered right
	// after home so it runs right before home's removal, fails the test if it
	// does instead of letting RemoveAll race the writer.
	var tuiPID string
	t.Cleanup(func() {
		if tuiPID != "" && processAlive(tuiPID) {
			t.Errorf("TUI pid %s still running when its HOME is removed", tuiPID)
		}
	})
	fakeBin := t.TempDir()
	// The fleet's tmux server and the TUI's share a private TMUX_TMPDIR that
	// cleanup kills, so later tests in the package see no server. Registered
	// after the t.TempDir calls so it runs before them (cleanups run last-in,
	// first-out), and it waits for the TUI to exit: kill-server only signals
	// it.
	socket, cleanup := testutil.ShortTmuxSocket()
	t.Cleanup(func() {
		cleanup()
		if tuiPID != "" {
			waitProcessExit(t, tuiPID, tuiExitDeadline)
		}
	})
	for name, script := range map[string]string{"claude": fakeClaude, "ssh": fakeSSH} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	env := append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"XDG_DATA_HOME="+filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME="+filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"TMUX_TMPDIR="+filepath.Dir(socket),
		"AGENTDECK_TELEMETRY=0", "DO_NOT_TRACK=1", "TERM=xterm-256color",
	)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("agent-deck %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}

	var config strings.Builder
	config.WriteString("[updates]\ncheck_enabled = false\n")
	for i := 1; i <= idleFleetRemotes; i++ {
		fmt.Fprintf(&config, "\n[remotes.r%d]\nhost = \"u@r%d\"\n", i, i)
	}
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".agent-deck", "config.toml"), []byte(config.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	run("hooks", "install")
	for i := 1; i <= idleFleetSessions; i++ {
		dir := filepath.Join(home, "proj", fmt.Sprintf("s%d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		title := fmt.Sprintf("s%d", i)
		run("add", "-c", "claude", "-t", title, "-g", "work", dir)
		run("session", "start", title)
	}
	hooksDir := filepath.Join(home, ".local", "share", "agent-deck", "hooks")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < idleFleetHookSessions; i++ {
		id := fmt.Sprintf("idle%05d-1791662346", i)
		for _, suffix := range []string{".json", ".sid", ".events.jsonl", ".generation.json", ".lock"} {
			if err := os.WriteFile(filepath.Join(hooksDir, id+suffix), []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The TUI gets its own tmux server so it has a terminal; TMUX is unset
	// inside so it drives the fleet's server like a TUI run from a shell.
	host := exec.Command("tmux", "-S", socket, "new-session", "-d", "-s", "tui", "-x", "200", "-y", "50",
		"exec env -u TMUX -u TMUX_PANE "+binary)
	host.Env = env
	if out, err := host.CombinedOutput(); err != nil {
		t.Fatalf("start TUI: %v\n%s", err, out)
	}
	out, err := exec.Command("tmux", "-S", socket, "display-message", "-p", "-t", "tui", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("TUI pid: %v", err)
	}
	pid := strings.TrimSpace(string(out))
	tuiPID = pid
	screen := func() string {
		out, _ := exec.Command("tmux", "-S", socket, "capture-pane", "-p", "-t", "tui").Output()
		return string(out)
	}
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(screen(), "s10") {
		if time.Now().After(deadline) {
			t.Fatalf("TUI did not list the fleet:\n%s", screen())
		}
		time.Sleep(200 * time.Millisecond)
	}

	time.Sleep(idleCPUWarmup)
	before := processCPUTime(t, pid)
	start := time.Now()
	time.Sleep(idleCPUWindow)
	used := processCPUTime(t, pid) - before
	percent := 100 * used.Seconds() / time.Since(start).Seconds()
	t.Logf("idle TUI (%d sessions, %d remotes, %d hook sessions): %v CPU over %v = %.1f%% of one core (budget %.0f%%)",
		idleFleetSessions, idleFleetRemotes, idleFleetHookSessions, used, idleCPUWindow, percent, idleCPUBudgetPercent)
	if budget := idleCPUBudgetPercent * testutil.BudgetMultiplier(t); percent > budget {
		t.Fatalf("idle TUI used %.1f%% of one core, budget %.1f%% (%.0f%% x PERF_BUDGET_MULTIPLIER)", percent, budget, idleCPUBudgetPercent)
	}
}

// processCPUTime returns the user plus system CPU time pid has used, from
// /proc/<pid>/stat (fields 14 and 15, in clock ticks of 1/100 s on Linux).
func processCPUTime(t *testing.T, pid string) time.Duration {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		t.Fatalf("read process CPU time: %v", err)
	}
	// The command name (field 2) may contain spaces; fields after it start
	// at the closing parenthesis.
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	var ticks int64
	for _, f := range fields[11:13] {
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			t.Fatalf("parse /proc/%s/stat: %v", pid, err)
		}
		ticks += n
	}
	return time.Duration(ticks) * 10 * time.Millisecond
}

// processAlive reports whether pid is a live (non-zombie) process.
func processAlive(pid string) bool {
	data, err := os.ReadFile(filepath.Join("/proc", pid, "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(data[strings.LastIndexByte(string(data), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z" && fields[0] != "X"
}

// waitProcessExit waits until pid has exited, failing the test at deadline.
func waitProcessExit(t *testing.T, pid string, deadline time.Duration) {
	t.Helper()
	end := time.Now().Add(deadline)
	for processAlive(pid) {
		if time.Now().After(end) {
			t.Errorf("TUI pid %s still running %v after its tmux server was killed", pid, deadline)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
