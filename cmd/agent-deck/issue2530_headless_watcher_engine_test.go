package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Issue #2530: `agent-deck web --no-tui` ran no watcher engine, so a watcher
// created and started from the CLI was never listened for. These tests drive
// the real binary. Each env is a throwaway profile with its own HOME, XDG
// dirs, runtime dir and tmux socket dir, so the processes it starts reach
// nothing outside it.

const issue2530Profile = "e2e2530"

type issue2530Env struct {
	t    *testing.T
	bin  string
	root string
	env  []string
	port string // the webhook watcher's port
}

func newIssue2530Env(t *testing.T) *issue2530Env {
	t.Helper()
	if testing.Short() {
		t.Skip("starts real agent-deck processes")
	}
	// A short root: the tmux socket path must stay under the 108-byte limit.
	root, err := os.MkdirTemp("", "ad2530-")
	if err != nil {
		t.Fatal(err)
	}
	e := &issue2530Env{t: t, bin: channelsCLIBinary(t), root: root}
	for _, d := range []string{"home", "config", "data", "cache", "state", "run", "tmux", "proj"} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "TMUX") || strings.HasPrefix(name, "AGENTDECK_") ||
			strings.HasPrefix(name, "AGENT_DECK_") || strings.HasPrefix(name, "XDG_") ||
			name == "HOME" || name == "SHELL" {
			continue
		}
		e.env = append(e.env, kv)
	}
	e.env = append(e.env,
		"HOME="+filepath.Join(root, "home"),
		"XDG_CONFIG_HOME="+filepath.Join(root, "config"),
		"XDG_DATA_HOME="+filepath.Join(root, "data"),
		"XDG_CACHE_HOME="+filepath.Join(root, "cache"),
		"XDG_STATE_HOME="+filepath.Join(root, "state"),
		"XDG_RUNTIME_DIR="+filepath.Join(root, "run"),
		"TMUX_TMPDIR="+filepath.Join(root, "tmux"),
		"SHELL=/bin/sh",
		"AGENTDECK_SKIP_UPDATE_CHECK=1",
		"TERM=dumb",
	)
	t.Cleanup(func() {
		kill := exec.Command("tmux", "kill-server")
		kill.Env = e.env
		_ = kill.Run()
		_ = os.RemoveAll(root)
	})
	return e
}

// cli runs one agent-deck command on the env's profile and returns its output.
func (e *issue2530Env) cli(args ...string) string {
	e.t.Helper()
	cmd := exec.Command(e.bin, append([]string{"-p", issue2530Profile}, args...)...)
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		e.t.Fatalf("agent-deck %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// createWebhookWatcher creates and starts a webhook watcher on a free port,
// with alice@example.com routed to conductor "demo".
func (e *issue2530Env) createWebhookWatcher() {
	e.t.Helper()
	e.port = issue2530FreePort(e.t)
	e.cli("watcher", "create", "webhook", "--name", "hook", "--port", e.port)
	e.cli("watcher", "start", "hook")
	var st struct {
		ConfigPath string `json:"config_path"`
	}
	if err := json.Unmarshal([]byte(e.cli("watcher", "status", "hook", "--json")), &st); err != nil || st.ConfigPath == "" {
		e.t.Fatalf("watcher status: %v (%q)", err, st.ConfigPath)
	}
	if !strings.HasPrefix(st.ConfigPath, e.root+string(filepath.Separator)) {
		e.t.Fatalf("watcher dir %q is outside the test root %q", st.ConfigPath, e.root)
	}
	clients := `{"alice@example.com": {"conductor": "demo", "name": "Alice"}}`
	if err := os.WriteFile(filepath.Join(filepath.Dir(st.ConfigPath), "clients.json"), []byte(clients), 0o600); err != nil {
		e.t.Fatal(err)
	}
}

// startWeb starts `agent-deck web --no-tui` in its own process group.
// Cleanup stops it if the test has not.
func (e *issue2530Env) startWeb(name string) *exec.Cmd {
	e.t.Helper()
	webPort := issue2530FreePort(e.t)
	cmd := exec.Command(e.bin, "-p", issue2530Profile, "web", "--no-tui", "--listen", "127.0.0.1:"+webPort)
	cmd.Env = e.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	logFile, err := os.Create(filepath.Join(e.root, name+".log"))
	if err != nil {
		e.t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		_ = logFile.Close()
		close(exited)
	}()
	e.t.Cleanup(func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-exited
	})
	issue2530WaitFor(e.t, 15*time.Second, name+" web server", func() bool {
		resp, err := http.Get("http://127.0.0.1:" + webPort + "/healthz")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	})
	return cmd
}

// lockPID returns the pid recorded in the profile's watcher engine lock, or 0.
func (e *issue2530Env) lockPID() int {
	b, err := os.ReadFile(filepath.Join(e.root, "data", "agent-deck", "runtime", "profiles", issue2530Profile, "watcher-engine.lock"))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return pid
}

func (e *issue2530Env) listening() bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:"+e.port, 200*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

func (e *issue2530Env) post(body string) {
	e.t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+e.port+"/webhook", strings.NewReader(body))
	if err != nil {
		e.t.Fatal(err)
	}
	req.Header.Set("X-Webhook-Sender", "alice@example.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatalf("POST to the webhook watcher: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		e.t.Fatalf("webhook answered %d", resp.StatusCode)
	}
}

// waitRecorded waits until `watcher status` lists the event routed to demo.
func (e *issue2530Env) waitRecorded(body string) {
	e.t.Helper()
	issue2530WaitFor(e.t, 10*time.Second, "event "+body+" recorded and routed to demo", func() bool {
		var st struct {
			Events []struct {
				Subject  string `json:"subject"`
				RoutedTo string `json:"routed_to"`
			} `json:"recent_events"`
		}
		if err := json.Unmarshal([]byte(e.cli("watcher", "status", "hook", "--json")), &st); err != nil {
			return false
		}
		for _, ev := range st.Events {
			if ev.Subject == body && ev.RoutedTo == "demo" {
				return true
			}
		}
		return false
	})
}

// TestIssue2530_HeadlessWebRunsTheWatcherEngine: with a webhook watcher
// created and started from the CLI, `web --no-tui` listens on the watcher's
// port, records and routes a POSTed event, and delivers it to the conductor's
// pane, which it finds in storage since no TUI loads the sessions.
func TestIssue2530_HeadlessWebRunsTheWatcherEngine(t *testing.T) {
	e := newIssue2530Env(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	e.createWebhookWatcher()
	var launched struct {
		TmuxSession string `json:"tmux_session"`
	}
	out := e.cli("launch", filepath.Join(e.root, "proj"), "-t", "conductor-demo", "-c", "cat >/dev/null", "--no-parent", "--json")
	if err := json.Unmarshal([]byte(out), &launched); err != nil || launched.TmuxSession == "" {
		t.Fatalf("launch conductor-demo: %v\n%s", err, out)
	}

	web := e.startWeb("web")
	issue2530WaitFor(t, 10*time.Second, "the watcher port to listen under web --no-tui", e.listening)
	if pid := e.lockPID(); pid != web.Process.Pid {
		t.Fatalf("watcher engine lock names pid %d, want the web process %d", pid, web.Process.Pid)
	}

	e.post("headless-2530")
	e.waitRecorded("headless-2530")
	issue2530WaitFor(t, 10*time.Second, "the event in the conductor pane", func() bool {
		capture := exec.Command("tmux", "capture-pane", "-p", "-t", launched.TmuxSession)
		capture.Env = e.env
		pane, _ := capture.Output()
		return strings.Contains(string(pane), "[webhook] alice@example.com: headless-2530")
	})
}

// TestIssue2530_HeadlessPicksUpTheFirstWatcherStartedLater: a `web --no-tui`
// started before any watcher exists holds no engine lock, so the first
// watcher created and started afterwards runs without a restart (review on
// #2638: a process with nothing to run must not keep the lock).
func TestIssue2530_HeadlessPicksUpTheFirstWatcherStartedLater(t *testing.T) {
	e := newIssue2530Env(t)
	web := e.startWeb("web")
	if pid := e.lockPID(); pid != 0 {
		t.Fatalf("a web process with no watcher holds the engine lock (pid %d)", pid)
	}
	e.createWebhookWatcher()
	issue2530WaitFor(t, 15*time.Second, "the web process to start the engine for the new watcher", func() bool {
		return e.lockPID() == web.Process.Pid && e.listening()
	})
	e.post("started-later")
	e.waitRecorded("started-later")
}

// TestIssue2530_HeadlessStopsTheEngineWhenTheWebServerFails: the engine is
// running by the time `server.Start` fails (here: its port is taken), and
// exitCLI skips deferred calls, so the failure path must stop the engine
// itself before exiting: release the lock after Engine.Stop and the conductor
// drain, not leave it to the kernel mid-delivery (review on #2638).
func TestIssue2530_HeadlessStopsTheEngineWhenTheWebServerFails(t *testing.T) {
	e := newIssue2530Env(t)
	e.createWebhookWatcher()
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()

	cmd := exec.Command(e.bin, "-p", issue2530Profile, "web", "--no-tui", "--listen", busy.Addr().String())
	cmd.Env = e.env
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
		t.Fatalf("web --no-tui on a taken port: %v, want exit status 1\n%s", err, out)
	}
	logData, _ := os.ReadFile(filepath.Join(e.root, "cache", "agent-deck", "debug.log"))
	log := string(logData)
	if !strings.Contains(log, `"msg":"watcher_engine_owner"`) {
		t.Fatalf("the engine never started, so this test proves nothing\n%s\n%s", out, log)
	}
	if !strings.Contains(log, `"msg":"watcher_engine_released"`) {
		t.Fatalf("web --no-tui exited on the web server error without stopping the watcher engine\n%s", out)
	}
}

// TestIssue2530_TwoProcessesRunOneEngineAndTheOtherTakesOver: two long-lived
// processes on one profile. Only the first binds the webhook port; the second
// waits. Once the owner exits, by signal or killed outright, the second takes
// over, binds the port and handles events.
func TestIssue2530_TwoProcessesRunOneEngineAndTheOtherTakesOver(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL} {
		t.Run(sig.String(), func(t *testing.T) {
			e := newIssue2530Env(t)
			e.createWebhookWatcher()

			first := e.startWeb("first")
			issue2530WaitFor(t, 10*time.Second, "the first process to bind the watcher port", e.listening)
			second := e.startWeb("second")
			// The standby retries every 5 s: give it one retry before checking
			// that it stayed off the port.
			time.Sleep(6 * time.Second)
			if pid := e.lockPID(); pid != first.Process.Pid {
				t.Fatalf("watcher engine lock names pid %d, want the first process %d", pid, first.Process.Pid)
			}
			if pid, ok := issue2530ListenerPID(e.port, first.Process.Pid, second.Process.Pid); ok && pid != first.Process.Pid {
				t.Fatalf("watcher port is held by pid %d, want only the first process %d", pid, first.Process.Pid)
			}
			e.post("while-first-owns")
			e.waitRecorded("while-first-owns")

			if err := first.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			issue2530WaitFor(t, 15*time.Second, "the second process to take the engine over", func() bool {
				return e.lockPID() == second.Process.Pid && e.listening()
			})
			if pid, ok := issue2530ListenerPID(e.port, first.Process.Pid, second.Process.Pid); ok && pid != second.Process.Pid {
				t.Fatalf("after the takeover the watcher port is held by pid %d, want the second process %d", pid, second.Process.Pid)
			}
			e.post("after-takeover")
			e.waitRecorded("after-takeover")
		})
	}
}

func issue2530FreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
}

func issue2530WaitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// issue2530ListenerPID returns which of pids holds the TCP socket listening
// on port, read from /proc. ok is false where /proc is not available.
func issue2530ListenerPID(port string, pids ...int) (pid int, ok bool) {
	p, err := strconv.Atoi(port)
	if err != nil {
		return 0, false
	}
	inodes := map[string]bool{}
	for _, table := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(table)
		if err != nil {
			continue
		}
		ok = true
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// sl local_address rem_address st ... inode (field 9)
			if len(fields) < 10 || fields[3] != "0A" {
				continue
			}
			if strings.HasSuffix(fields[1], fmt.Sprintf(":%04X", p)) {
				inodes["socket:["+fields[9]+"]"] = true
			}
		}
		_ = f.Close()
	}
	if !ok {
		return 0, false
	}
	for _, candidate := range pids {
		fds, _ := filepath.Glob(fmt.Sprintf("/proc/%d/fd/*", candidate))
		for _, fd := range fds {
			if target, err := os.Readlink(fd); err == nil && inodes[target] {
				return candidate, true
			}
		}
	}
	return 0, true
}
