package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// Issue #2524: a routed watcher event (and a watcher health alert) must reach
// the conductor while the TUI is attached to a session. Attaching goes
// through tea.Exec, and Bubble Tea runs an exec message synchronously on its
// event loop, so no message reaches Home.Update until the attach returns.
//
// These tests drive the production wiring end to end: startWatcherEngine
// builds the engine from statedb (a real webhook adapter on a loopback port,
// the router from clients.json), a real Bubble Tea program hands Home's
// watcher messages to Home.Update, and the conductor is a real tmux pane on a
// dedicated socket. The program then blocks in tea.Exec, exactly as an attach
// does, while the watcher produces the event.
//
// Since #2537 the engine queues a routed event for its conductor in the
// profile's send queue, and a detached `session send-worker` types it (the
// cmd/agent-deck TestIssue2537_* tests deliver through the real binary). Here
// the worker start is recorded instead, so the event tests check the queue;
// health alerts still go to the pane from this process.

const issue2524Conductor = "demo"

// issue2524Exec is an ExecCommand that holds the program in tea.Exec until
// released, standing in for the attach client.
type issue2524Exec struct {
	started chan struct{}
	release chan struct{}
}

func (e *issue2524Exec) Run() error {
	close(e.started)
	<-e.release
	return nil
}

func (e *issue2524Exec) SetStdin(io.Reader)  {}
func (e *issue2524Exec) SetStdout(io.Writer) {}
func (e *issue2524Exec) SetStderr(io.Writer) {}

type issue2524AttachMsg struct{ exec *issue2524Exec }

type issue2524DetachedMsg struct{}

// issue2524Model hands the watcher listener messages to the real Home.Update,
// so every Home-side watcher path runs on the program's event loop as in
// production, and attaches through tea.Exec on request.
type issue2524Model struct {
	home    *Home
	init    tea.Cmd
	handled chan tea.Msg
}

func (m *issue2524Model) Init() tea.Cmd { return m.init }
func (m *issue2524Model) View() string  { return "" }

func (m *issue2524Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case issue2524AttachMsg:
		return m, tea.Exec(msg.exec, func(error) tea.Msg { return issue2524DetachedMsg{} })
	case watcherEventMsg, watcherHealthMsg:
		_, cmd := m.home.Update(msg)
		select {
		case m.handled <- msg:
		default:
		}
		return m, cmd
	}
	return m, nil
}

type issue2524Env struct {
	home      *Home
	db        *statedb.StateDB
	conductor *session.Instance
	socket    string
	pane      string
	handled   chan tea.Msg
	spawned   *issue2524Spawns
}

// issue2524Spawns records the send workers the engine starts.
type issue2524Spawns struct {
	mu      sync.Mutex
	targets []string
}

func (s *issue2524Spawns) spawn(_, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets = append(s.targets, sessionID)
	return nil
}

func (s *issue2524Spawns) started(sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Contains(s.targets, sessionID)
}

// newIssue2524Env prepares an isolated profile with one webhook watcher whose
// [source] settings are given, routes alice@example.com to the demo conductor,
// and starts a tmux pane for that conductor running paneCmd (default: a pane
// that echoes whatever is typed).
func newIssue2524Env(t *testing.T, watcherName string, source map[string]string, configTOML, paneCmd string) *issue2524Env {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	configDir := setIsolatedAgentDeckDir(t)
	tmpHome := os.Getenv("HOME")
	// Pin the other XDG roots under this HOME too: a value another test left
	// in the environment would put the watcher layout somewhere else.
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, ".local", "state"))
	if configTOML != "" {
		if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(configTOML), 0o600); err != nil {
			t.Fatal(err)
		}
		session.ClearUserConfigCache()
	}

	db, err := statedb.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatal(err)
	}
	prevDB := statedb.GetGlobal()
	statedb.SetGlobal(db)
	t.Cleanup(func() {
		statedb.SetGlobal(prevDB)
		_ = db.Close()
	})
	now := time.Now()
	if err := db.SaveWatcher(&statedb.WatcherRow{
		ID: "w-" + watcherName, Name: watcherName, Type: "webhook", Status: "running",
		Conductor: issue2524Conductor, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	watcherDir, err := session.WatcherDir()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(watcherDir, tmpHome+string(filepath.Separator)) {
		t.Fatalf("watcher dir %q is outside the isolated HOME %q", watcherDir, tmpHome)
	}
	if err := os.MkdirAll(filepath.Join(watcherDir, watcherName), 0o700); err != nil {
		t.Fatal(err)
	}
	var toml strings.Builder
	toml.WriteString("[source]\n")
	for k, v := range source {
		fmt.Fprintf(&toml, "%s = %q\n", k, v)
	}
	if err := os.WriteFile(filepath.Join(watcherDir, watcherName, "watcher.toml"), []byte(toml.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	clients, _ := json.Marshal(map[string]watcher.ClientEntry{
		"alice@example.com": {Conductor: issue2524Conductor, Name: "Alice"},
	})
	if err := os.WriteFile(filepath.Join(watcherDir, "clients.json"), clients, 0o600); err != nil {
		t.Fatal(err)
	}

	// The conductor pane: a dedicated tmux socket, never the user's server.
	socket := fmt.Sprintf("agentdeck-2524-%d-%d", os.Getpid(), time.Now().UnixNano())
	ts := tmux.NewSession(session.ConductorSessionTitle(issue2524Conductor), t.TempDir())
	ts.SocketName = socket
	if paneCmd == "" {
		paneCmd = "cat >/dev/null"
	}
	start := exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-x", "200", "-y", "30",
		"-s", ts.Name, paneCmd)
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start conductor pane: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	inst := session.NewInstanceWithTool(session.ConductorSessionTitle(issue2524Conductor), t.TempDir(), "shell")
	inst.SetTmuxSessionForTest(ts)
	// The engine finds the conductor to queue for in storage (#2537).
	if err := db.SaveInstance(&statedb.InstanceRow{
		ID: inst.ID, Title: inst.Title, Tool: inst.Tool, ProjectPath: inst.ProjectPath, GroupPath: "conductors",
		Status: "running", TmuxSession: ts.Name, TmuxSocketName: socket, CreatedAt: now, LastAccessed: now,
	}); err != nil {
		t.Fatal(err)
	}
	spawned := &issue2524Spawns{}
	home := NewHome()
	home.spawnSendWorker = spawned.spawn
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()

	return &issue2524Env{home: home, db: db, conductor: inst, socket: socket, pane: ts.Name, handled: make(chan tea.Msg, 64), spawned: spawned}
}

// queued returns the send queue records whose message is want, oldest first.
func (e *issue2524Env) queued(t *testing.T, want string) []*sendqueue.Record {
	t.Helper()
	recs, err := sendqueue.List(sendqueue.Dir(filepath.Dir(e.db.Path())), "")
	if err != nil {
		t.Fatal(err)
	}
	var out []*sendqueue.Record
	for _, r := range recs {
		if r.Message == want {
			out = append(out, r)
		}
	}
	return out
}

// waitQueued reports whether the message want is queued for the conductor,
// with its worker started, in time.
func (e *issue2524Env) waitQueued(t *testing.T, want string, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if recs := e.queued(t, want); len(recs) > 0 && recs[0].SessionID == e.conductor.ID && e.spawned.started(e.conductor.ID) {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// run starts the watcher engine through Home and a Bubble Tea program around
// Home. Cleanup quits the program and stops the engine.
func (e *issue2524Env) run(t *testing.T) *tea.Program {
	t.Helper()
	initCmd := e.home.startWatcherEngine()
	if host := e.home.watcherHost.Load(); host == nil || host.Engine() == nil {
		t.Fatal("startWatcherEngine did not start an engine")
	}
	model := &issue2524Model{home: e.home, init: initCmd, handled: e.handled}
	// A pipe nobody writes to: the input reader exists (tea.Exec restores it
	// after the command) but never produces a key.
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prog := tea.NewProgram(model, tea.WithInput(inR), tea.WithOutput(io.Discard),
		tea.WithoutRenderer(), tea.WithoutSignalHandler())
	done := make(chan struct{})
	go func() {
		_, _ = prog.Run()
		close(done)
	}()
	t.Cleanup(func() {
		prog.Quit()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("program did not quit")
		}
		_ = inW.Close()
		_ = inR.Close()
		e.home.StopWatcherEngine()
	})
	return prog
}

// attach puts the program into tea.Exec and waits until the exec command runs.
// The returned func ends the attach; cleanup ends it too.
func (e *issue2524Env) attach(t *testing.T, prog *tea.Program) (detach func()) {
	t.Helper()
	ex := &issue2524Exec{started: make(chan struct{}), release: make(chan struct{})}
	released := false
	detach = func() {
		if !released {
			released = true
			close(ex.release)
		}
	}
	t.Cleanup(detach)
	prog.Send(issue2524AttachMsg{exec: ex})
	select {
	case <-ex.started:
	case <-time.After(5 * time.Second):
		t.Fatal("program did not enter tea.Exec")
	}
	return detach
}

func (e *issue2524Env) paneCount(needle string) int {
	out, err := exec.Command("tmux", "-L", e.socket, "capture-pane", "-p", "-J", "-S", "-", "-t", e.pane).Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), needle)
}

// waitForPane reports whether needle shows up in the conductor pane in time.
func (e *issue2524Env) waitForPane(needle string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e.paneCount(needle) > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func issue2524FreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return strconv.Itoa(port)
}

// postWebhook delivers one event to the webhook watcher, retrying only while
// the adapter is not listening yet.
func postWebhook(t *testing.T, port, sender, body string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:"+port+"/webhook", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Webhook-Sender", sender)
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusAccepted {
				t.Fatalf("webhook answered %d", resp.StatusCode)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook watcher never accepted the event: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestIssue2524_WatcherEventIsQueuedForTheConductorWhileHomeIsInExec(t *testing.T) {
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2524", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")
	prog := env.run(t)
	detach := env.attach(t, prog)

	marker := fmt.Sprintf("issue-2524-event-%d", time.Now().UnixNano())
	postWebhook(t, port, "alice@example.com", marker)
	want := "[webhook] alice@example.com: " + marker

	if !env.waitQueued(t, want, 5*time.Second) {
		t.Fatal("routed watcher event was not queued for the conductor within 5s while Home was in tea.Exec (attached)")
	}

	// Ending the attach lets Home.Update see the queued watcherEventMsg. It
	// may refresh the panel, but the event must not be queued again.
	detach()
	select {
	case <-env.handled:
	case <-time.After(5 * time.Second):
		t.Fatal("Home.Update never received the watcher event after the attach ended")
	}
	time.Sleep(time.Second)
	if n := len(env.queued(t, want)); n != 1 {
		t.Fatalf("watcher event queued %d times, want exactly once", n)
	}
}

func TestIssue2524_HealthAlertReachesConductorWhileHomeIsInExec(t *testing.T) {
	// 192.0.2.1 (TEST-NET-1) is never a local address: the webhook cannot
	// bind it and its health check cannot reach it, so the watcher reports
	// an error on every health tick.
	env := newIssue2524Env(t, "hook-2524-down",
		map[string]string{"bind": "192.0.2.1", "port": issue2524FreePort(t)},
		"[watcher]\nhealth_check_interval_seconds = 1\n", "")
	prog := env.run(t)
	env.attach(t, prog)

	want := `[WATCHER HEALTH ALERT] Watcher "hook-2524-down" transitioned to error`
	if !env.waitForPane(want, 6*time.Second) {
		t.Fatal("watcher health alert did not reach the conductor pane within 6s while Home was in tea.Exec (attached)")
	}
}

// TestIssue2524_EventBurstIsQueuedInOrderOncePerEvent: the engine routes a
// burst of events within microseconds. Each becomes its own record, in the
// order it arrived: the conductor's send worker types one record at a time
// (under the target's send lock), so they reach the conductor as separate
// submissions in that order instead of merging into one command.
func TestIssue2524_EventBurstIsQueuedInOrderOncePerEvent(t *testing.T) {
	port := issue2524FreePort(t)
	env := newIssue2524Env(t, "hook-2524-burst", map[string]string{"bind": "127.0.0.1", "port": port}, "", "")
	prog := env.run(t)
	env.attach(t, prog)

	const burst = 5
	var want []string
	stamp := time.Now().UnixNano()
	for i := 1; i <= burst; i++ {
		marker := fmt.Sprintf("burst-%d-%d", stamp, i)
		postWebhook(t, port, "alice@example.com", marker)
		want = append(want, "[webhook] alice@example.com: "+marker)
	}
	if !env.waitQueued(t, want[burst-1], 10*time.Second) {
		t.Fatal("the last event of the burst was not queued")
	}
	recs, err := sendqueue.List(sendqueue.Dir(filepath.Dir(env.db.Path())), env.conductor.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range recs {
		got = append(got, r.Message)
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("queued %d records, want %d, one per event in order:\ngot:\n  %s\nwant:\n  %s",
			len(got), burst, strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}
