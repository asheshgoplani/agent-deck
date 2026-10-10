package ui

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/asheshgoplani/agent-deck/internal/watcher"
)

// Issue #2531: a watcher that goes to warning or error must tell its
// conductor once per transition.
//
//   - Part 1: the conductor came only from the watchers.conductor column,
//     which `watcher create` writes empty and nothing else sets, so no alert
//     ever reached a conductor. The conductor that receives the watcher's
//     routed events (clients.json) must receive its health alerts too.
//   - Part 2: the engine reports every adapter's state on every health tick
//     and every warning/error report became an alert, so a watcher that stayed
//     down alerted on every tick.
//
// These tests drive the production wiring end to end: startWatcherEngine
// builds the engine from statedb with a real webhook adapter that cannot bind
// (so it reports error on every tick), a Bubble Tea program hands Home the
// watcher messages, and the conductor is a real tmux pane on a private socket.

const issue2531Conductor = "demo"

// issue2531Model hands watcher listener messages to the real Home.Update, as
// the TUI does.
type issue2531Model struct {
	home *Home
	init tea.Cmd
}

func (m *issue2531Model) Init() tea.Cmd { return m.init }
func (m *issue2531Model) View() string  { return "" }

func (m *issue2531Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg.(type) {
	case watcherEventMsg, watcherHealthMsg:
		_, cmd := m.home.Update(msg)
		return m, cmd
	}
	return m, nil
}

type issue2531Env struct {
	home   *Home
	socket string
	pane   string
}

// newIssue2531Env prepares an isolated profile with one running webhook
// watcher that cannot bind its address, a 1s health interval, a clients.json
// that routes alice@example.com to the demo conductor, and a tmux pane for
// that conductor. columnConductor is written to watchers.conductor ("" is what
// `watcher create` writes).
func newIssue2531Env(t *testing.T, watcherName, columnConductor string) *issue2531Env {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}

	configDir := setIsolatedAgentDeckDir(t)
	tmpHome := os.Getenv("HOME")
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, ".local", "state"))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"),
		[]byte("[watcher]\nhealth_check_interval_seconds = 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	session.ClearUserConfigCache()

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
		Conductor: columnConductor, CreatedAt: now, UpdatedAt: now,
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
	// 192.0.2.1 (TEST-NET-1) is never a local address: the webhook cannot
	// bind it and its health check cannot reach it.
	toml := fmt.Sprintf("[source]\nbind = %q\nport = %q\n", "192.0.2.1", issue2531FreePort(t))
	if err := os.WriteFile(filepath.Join(watcherDir, watcherName, "watcher.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	clients, _ := json.Marshal(map[string]watcher.ClientEntry{
		"alice@example.com": {Conductor: issue2531Conductor, Name: "Alice"},
	})
	if err := os.WriteFile(filepath.Join(watcherDir, "clients.json"), clients, 0o600); err != nil {
		t.Fatal(err)
	}

	// The conductor pane: a dedicated tmux socket, never the user's server.
	socket := fmt.Sprintf("agentdeck-2531-%d-%d", os.Getpid(), time.Now().UnixNano())
	ts := tmux.NewSession(session.ConductorSessionTitle(issue2531Conductor), t.TempDir())
	ts.SocketName = socket
	start := exec.Command("tmux", "-u", "-L", socket, "new-session", "-d", "-x", "220", "-y", "40",
		"-s", ts.Name, "cat >/dev/null")
	if out, err := start.CombinedOutput(); err != nil {
		t.Fatalf("start conductor pane: %v: %s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", socket, "kill-server").Run() })

	inst := session.NewInstanceWithTool(session.ConductorSessionTitle(issue2531Conductor), t.TempDir(), "shell")
	inst.SetTmuxSessionForTest(ts)
	home := NewHome()
	home.instancesMu.Lock()
	home.instances = []*session.Instance{inst}
	home.instanceByID = map[string]*session.Instance{inst.ID: inst}
	home.instancesMu.Unlock()

	return &issue2531Env{home: home, socket: socket, pane: ts.Name}
}

// run starts the watcher engine through Home and a Bubble Tea program around
// it. Cleanup quits the program and stops the engine.
func (e *issue2531Env) run(t *testing.T) {
	t.Helper()
	initCmd := e.home.startWatcherEngine()
	if e.home.watcherEngine == nil {
		t.Fatal("startWatcherEngine did not start an engine")
	}
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	prog := tea.NewProgram(&issue2531Model{home: e.home, init: initCmd}, tea.WithInput(inR),
		tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
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
		e.home.watcherEngine.Stop()
	})
}

func (e *issue2531Env) paneCount(needle string) int {
	out, err := exec.Command("tmux", "-L", e.socket, "capture-pane", "-p", "-J", "-S", "-", "-t", e.pane).Output()
	if err != nil {
		return 0
	}
	return strings.Count(string(out), needle)
}

func (e *issue2531Env) waitForPane(needle string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if e.paneCount(needle) > 0 {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func issue2531FreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return strconv.Itoa(port)
}

// issue2531Window is how long a watcher stays down after its first alert. At a
// 1s health interval that is a dozen more error reports, and it outlasts one
// full conductor delivery (about 10s against a pane with no agent), so a
// repeated alert waiting behind the first one has time to arrive.
const issue2531Window = 12 * time.Second

func issue2531AlertText(name string) string {
	return fmt.Sprintf(`[WATCHER HEALTH ALERT] Watcher %q transitioned to error`, name)
}

// TestIssue2531_HealthAlertReachesTheConductorTheWatcherRoutesTo is part 1:
// the watcher row has no conductor (as `watcher create` leaves it), and the
// conductor its events route to must still get the alert.
func TestIssue2531_HealthAlertReachesTheConductorTheWatcherRoutesTo(t *testing.T) {
	const name = "hook-2531-routed"
	env := newIssue2531Env(t, name, "")
	env.run(t)

	want := issue2531AlertText(name)
	if !env.waitForPane(want, 8*time.Second) {
		t.Fatalf("no health alert reached conductor %q within 8s of the watcher going to error; "+
			"the watcher row has no conductor and clients.json routes its events to %q",
			issue2531Conductor, issue2531Conductor)
	}
	time.Sleep(issue2531Window)
	if n := env.paneCount(want); n != 1 {
		t.Fatalf("health alert delivered %d times while the watcher stayed in error, want exactly 1", n)
	}
}

// TestIssue2531_HealthAlertIsSentOncePerTransition is part 2: with the
// conductor set on the watcher row (the column still wins when set), a
// watcher that stays in error must alert once, not on every health tick.
func TestIssue2531_HealthAlertIsSentOncePerTransition(t *testing.T) {
	const name = "hook-2531-column"
	env := newIssue2531Env(t, name, issue2531Conductor)
	env.run(t)

	want := issue2531AlertText(name)
	if !env.waitForPane(want, 8*time.Second) {
		t.Fatal("no health alert reached the conductor named on the watcher row within 8s")
	}
	time.Sleep(issue2531Window)
	if n := env.paneCount(want); n != 1 {
		t.Fatalf("health alert delivered %d times in %s while the watcher stayed in error, want exactly 1 (one per transition, not per health tick)",
			n, issue2531Window)
	}
}

// issue2531Home is a Home whose conductor deliveries are recorded instead of
// typed into a pane, with an isolated profile and state database holding one
// watcher row. clients is written as clients.json when non-nil.
func issue2531Home(t *testing.T, row *statedb.WatcherRow, clients map[string]watcher.ClientEntry) (*Home, *sentLog, *statedb.StateDB) {
	t.Helper()
	setIsolatedAgentDeckDir(t)
	tmpHome := os.Getenv("HOME")
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmpHome, ".cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmpHome, ".local", "state"))

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
	row.CreatedAt, row.UpdatedAt = time.Now(), time.Now()
	if err := db.SaveWatcher(row); err != nil {
		t.Fatal(err)
	}
	if clients != nil {
		dir, err := session.WatcherDir()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(dir, tmpHome+string(filepath.Separator)) {
			t.Fatalf("watcher dir %q is outside the isolated HOME %q", dir, tmpHome)
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(clients)
		if err := os.WriteFile(filepath.Join(dir, "clients.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	sent := &sentLog{}
	home := NewHome()
	home.conductorDeliveriesOnce.Do(func() {
		home.conductorDeliveries = newConductorQueue(sent.add)
	})
	return home, sent, db
}

func issue2531Sent(t *testing.T, home *Home, sent *sentLog) []conductorDelivery {
	t.Helper()
	queueIdle(t, home.conductorDeliveries)
	sent.mu.Lock()
	defer sent.mu.Unlock()
	return append([]conductorDelivery(nil), sent.sent...)
}

// TestIssue2531_AlertsOnTransitionsAndSendsOneRecoveryNotice feeds the
// relay's health dispatcher the reports a watcher produces tick by tick.
func TestIssue2531_AlertsOnTransitionsAndSendsOneRecoveryNotice(t *testing.T) {
	const name = "slack-2531"
	home, sent, _ := issue2531Home(t, &statedb.WatcherRow{ID: "w-1", Name: name, Type: "slack", Status: "running"},
		map[string]watcher.ClientEntry{"slack:C123": {Conductor: "ops"}})
	report := func(status watcher.HealthStatus, msg string) {
		home.dispatchHealthAlert(watcher.HealthState{WatcherName: name, Status: status, Message: msg})
		// A newer alert for a watcher replaces one still queued; wait for each
		// to go out so every message the dispatcher produced is recorded.
		queueIdle(t, home.conductorDeliveries)
	}

	report(watcher.HealthStatusHealthy, "") // first report, healthy: nothing to say
	for i := 0; i < 5; i++ {
		report(watcher.HealthStatusWarning, fmt.Sprintf("no events for %d minutes", 61+i))
	}
	report(watcher.HealthStatusError, "adapter unhealthy") // escalation
	report(watcher.HealthStatusError, "adapter unhealthy")
	for i := 0; i < 3; i++ {
		report(watcher.HealthStatusHealthy, "")
	}
	report(watcher.HealthStatusWarning, "no events for 61 minutes") // down again

	got := issue2531Sent(t, home, sent)
	want := []string{
		`[WATCHER HEALTH ALERT] Watcher "slack-2531" transitioned to warning: no events for 61 minutes.`,
		`[WATCHER HEALTH ALERT] Watcher "slack-2531" transitioned to error: adapter unhealthy.`,
		`[WATCHER HEALTH RECOVERED] Watcher "slack-2531" is healthy again (was error).`,
		`[WATCHER HEALTH ALERT] Watcher "slack-2531" transitioned to warning: no events for 61 minutes.`,
	}
	if len(got) != len(want) {
		t.Fatalf("got %d conductor messages, want %d: %q", len(got), len(want), texts(got))
	}
	for i, d := range got {
		if d.Conductor != "ops" {
			t.Errorf("message %d went to %q, want the routed conductor \"ops\"", i, d.Conductor)
		}
		if !strings.HasPrefix(d.Text, want[i]) {
			t.Errorf("message %d = %q, want prefix %q", i, d.Text, want[i])
		}
	}
}

// TestIssue2531_ConductorFollowsTheWatchersRoutedEvents: with several
// conductors in clients.json, the alert goes where this watcher's newest
// routed event went (triage markers are not conductors). Until a conductor
// can be named nothing is sent, and the alert goes out once one can be.
func TestIssue2531_ConductorFollowsTheWatchersRoutedEvents(t *testing.T) {
	const name = "ntfy-2531"
	home, sent, db := issue2531Home(t, &statedb.WatcherRow{ID: "w-2", Name: name, Type: "ntfy", Status: "running"},
		map[string]watcher.ClientEntry{
			"ntfy:alerts@ntfy.sh": {Conductor: "ops"},
			"*@example.com":       {Conductor: "sales"},
		})
	down := watcher.HealthState{WatcherName: name, Status: watcher.HealthStatusError, Message: "adapter unhealthy"}

	home.dispatchHealthAlert(down)
	if got := issue2531Sent(t, home, sent); len(got) != 0 {
		t.Fatalf("sent %q with two candidate conductors and no routed events, want nothing", texts(got))
	}

	if _, err := db.SaveWatcherEvent("w-2", "k1", "ntfy:alerts@ntfy.sh", "s", "ops", "", "b", 100); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // created_at has one-second resolution
	if _, err := db.SaveWatcherEvent("w-2", "k2", "nobody@else.org", "s", "triage", "", "b", 100); err != nil {
		t.Fatal(err)
	}
	home.dispatchHealthAlert(down)
	home.dispatchHealthAlert(down)

	got := issue2531Sent(t, home, sent)
	if len(got) != 1 || got[0].Conductor != "ops" {
		t.Fatalf("got %d messages %q (first to %q), want one alert to \"ops\", the conductor of the watcher's newest routed event",
			len(got), texts(got), func() string {
				if len(got) > 0 {
					return got[0].Conductor
				}
				return ""
			}())
	}
}
