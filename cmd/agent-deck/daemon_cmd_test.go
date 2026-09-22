package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/core"
	"github.com/asheshgoplani/agent-deck/internal/core/daemon"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
	_ "modernc.org/sqlite"
)

// Slice 5 of docs/CORE-PLAN.md: `agent-deck daemon serve|status|stop`. These
// tests drive the real binary in a sandbox HOME.

type daemonStatusJSON struct {
	State  string         `json:"state"`
	PID    int            `json:"pid"`
	Socket string         `json:"socket"`
	Status *daemon.Status `json:"status"`
}

func daemonStatus(t *testing.T, home string, env []string) (daemonStatusJSON, int) {
	t.Helper()
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "daemon", "status", "--json")
	var st daemonStatusJSON
	if err := json.Unmarshal([]byte(stdout), &st); err != nil {
		t.Fatalf("daemon status --json: exit %d, %v\n%s\n%s", code, err, stdout, stderr)
	}
	return st, code
}

// startDaemon runs `agent-deck daemon serve` in the background and waits
// until it answers. The cleanup stops it if the test did not.
func startDaemon(t *testing.T, home string, env []string) (*exec.Cmd, daemonStatusJSON) {
	t.Helper()
	cmd := exec.Command(channelsCLIBinary(t), "daemon", "serve")
	cmd.Env = agentDeckTestEnv(home, env)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		st, code := daemonStatus(t, home, env)
		if code == 0 && st.State == string(daemon.StateRunning) && st.PID == cmd.Process.Pid {
			return cmd, st
		}
		select {
		case <-exited:
			t.Fatalf("daemon serve exited early:\n%s", out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon not running after 15s (last state %q):\n%s", st.State, out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// canonicalEnvelope renders an envelope with sorted keys, compact, with the
// request id and time values scrubbed. In particular, cost counters and tmux
// names remain visible to the byte comparison.
func canonicalEnvelope(t *testing.T, raw []byte) string {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatalf("decode envelope %q: %v", raw, err)
	}
	if _, ok := m["request_id"].(string); !ok {
		t.Fatalf("envelope without request_id: %s", raw)
	}
	m["request_id"] = "<ID>"
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	s := equivTimestamp.ReplaceAllString(string(b), "<TS>")
	return equivStartedAgo.ReplaceAllString(s, "started Ns ago")
}

func seedSessions(t *testing.T, home string, env []string, titles ...string) {
	t.Helper()
	for _, title := range titles {
		dir := filepath.Join(home, "proj", title)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, stderr, code := runAgentDeckEnv(t, home, "", env, "add", dir, "-t", title, "-c", "bash", "-g", "work"); code != 0 {
			t.Fatalf("seed add %s: exit %d: %s", title, code, stderr)
		}
	}
	// A never-started session reads idle for 1.5s after CreatedAt, then
	// error: age the seed past that window so no case straddles it.
	time.Sleep(2 * time.Second)
}

// TestDaemonEnvelopesMatchArgv is the slice-5 equivalence proof: every
// registered command, run over the socket and through argv with
// --json=envelope against the same store, returns the same canonical
// envelope. Mutating commands are paired on the same starting state (argv
// first, then the socket after the state is put back).
func TestDaemonEnvelopesMatchArgv(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := shortTempDir(t, "adeq")
	tmuxDir := shortTempDir(t, "adeqt")
	env := []string{"TMUX_TMPDIR=" + tmuxDir}
	t.Cleanup(func() {
		runAgentDeckEnv(t, home, "", env, "session", "stop", "alpha")
		testutil.KillTmuxServersUnder(tmuxDir)
	})
	seedSessions(t, home, env, "alpha", "beta")
	_, st := startDaemon(t, home, env)

	type step struct {
		argv []string
		id   string
		in   any
		// ok is the outcome the step must have, so the comparison cannot
		// pass because both sides failed alike.
		ok bool
	}
	argv := func(args ...string) []string { return append(args, "--json=envelope") }
	listIn := core.SessionListIn{LiveStatus: true}
	start := core.SessionStartIn{Session: "alpha"}
	stop := core.SessionStopIn{Session: "alpha"}
	// Each pair runs argv then socket; "setup" steps restore the state the
	// argv side started from before the socket side runs.
	type pair struct {
		name  string
		a     step
		setup []step
	}
	pairs := []pair{
		{name: "list, nothing running", a: step{argv("list"), core.IDSessionList, listIn, true}},
		{name: "group list", a: step{argv("group", "list"), core.IDGroupList, core.GroupListIn{}, true}},
		{name: "start unknown", a: step{argv("session", "start", "nosuch"), core.IDSessionStart, core.SessionStartIn{Session: "nosuch"}, false}},
		{name: "stop not running", a: step{argv("session", "stop", "alpha"), core.IDSessionStop, stop, false}},
		{name: "restart missing arg", a: step{argv("session", "restart"), core.IDSessionRestart, core.SessionRestartIn{}, false}},
		{name: "restart unknown", a: step{argv("session", "restart", "nosuch"), core.IDSessionRestart, core.SessionRestartIn{Session: "nosuch"}, false}},
		{
			name:  "start",
			a:     step{argv("session", "start", "alpha"), core.IDSessionStart, start, true},
			setup: []step{{argv: argv("session", "stop", "alpha"), ok: true}},
		},
		{name: "start already running", a: step{argv("session", "start", "alpha"), core.IDSessionStart, start, false}},
		{name: "list, alpha running", a: step{argv("list"), core.IDSessionList, listIn, true}},
		{name: "group list, alpha running", a: step{argv("group", "list"), core.IDGroupList, core.GroupListIn{}, true}},
		{name: "restart fresh session is skipped", a: step{argv("session", "restart", "alpha"), core.IDSessionRestart, core.SessionRestartIn{Session: "alpha"}, true}},
		{name: "restart --force", a: step{argv("session", "restart", "alpha", "--force"), core.IDSessionRestart, core.SessionRestartIn{Session: "alpha", Force: true}, true}},
		{
			name:  "stop",
			a:     step{argv("session", "stop", "alpha"), core.IDSessionStop, stop, true},
			setup: []step{{argv: argv("session", "start", "alpha"), ok: true}},
		},
	}

	runArgv := func(s step, viaDaemon bool) []byte {
		writeCoreDaemonModeConfig(t, home, viaDaemon)
		stdout, stderr, _ := runAgentDeckEnv(t, home, "", env, s.argv...)
		if stdout == "" {
			t.Fatalf("%v: no envelope on stdout (stderr %q)", s.argv, stderr)
		}
		return []byte(stdout)
	}
	checkOK := func(what string, raw []byte, want bool) {
		var env core.Envelope
		if err := json.Unmarshal(raw, &env); err != nil {
			t.Fatalf("%s: %v\n%s", what, err, raw)
		}
		if env.OK != want {
			t.Errorf("%s: ok=%v, want %v\n%s", what, env.OK, want, raw)
		}
	}

	covered := map[string]bool{}
	for _, p := range pairs {
		fromArgv := runArgv(p.a, false)
		checkOK(p.name+" (argv)", fromArgv, p.a.ok)
		for _, s := range p.setup {
			checkOK(p.name+" setup "+strings.Join(s.argv, " "), runArgv(s, false), s.ok)
		}
		fromDaemon := runArgv(p.a, true)
		checkOK(p.name+" (daemon CLI)", fromDaemon, p.a.ok)
		a := canonicalEnvelope(t, fromArgv)
		s := canonicalEnvelope(t, fromDaemon)
		if a != s {
			t.Errorf("%s: envelopes differ\nargv:   %s\ndaemon: %s", p.name, a, s)
		}
		covered[p.a.id] = true
	}
	for _, d := range coreRegistry().Defs() {
		if !covered[d.ID] {
			t.Errorf("registered command %s has no socket/argv equivalence case", d.ID)
		}
	}

	client, err := daemon.Dial(context.Background(), st.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	cmds, err := client.Catalog()
	if err != nil {
		t.Fatal(err)
	}
	if len(cmds) != len(coreRegistry().Defs()) {
		t.Errorf("socket catalog has %d commands, CLI registry %d", len(cmds), len(coreRegistry().Defs()))
	}
}

func writeCoreDaemonModeConfig(t *testing.T, home string, enabled bool) {
	t.Helper()
	dir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[core]\ndaemon = "+strconv.FormatBool(enabled)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCoreDaemonConfig(t *testing.T, home string) {
	writeCoreDaemonModeConfig(t, home, true)
}

// The real daemon and a direct argv writer must wait on the same profile
// lock before either can load, decide, spawn, or save.
func TestDaemonAndDirectCLIShareMutationLock(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := shortTempDir(t, "adlk")
	tmuxDir := shortTempDir(t, "adlkt")
	env := []string{"TMUX_TMPDIR=" + tmuxDir}
	t.Cleanup(func() { testutil.KillTmuxServersUnder(tmuxDir) })
	t.Cleanup(func() {
		runAgentDeckEnv(t, home, "", env, "session", "stop", "alpha")
		runAgentDeckEnv(t, home, "", env, "session", "stop", "beta")
	})
	seedSessions(t, home, env, "alpha", "beta")
	serialHome := shortTempDir(t, "adls")
	if out, err := exec.Command("cp", "-a", home+"/.", serialHome).CombinedOutput(); err != nil {
		t.Fatalf("clone seeded store: %v: %s", err, out)
	}
	serialTmuxDir := shortTempDir(t, "adlst")
	serialEnv := []string{"TMUX_TMPDIR=" + serialTmuxDir}
	t.Cleanup(func() { testutil.KillTmuxServersUnder(serialTmuxDir) })
	_, st := startDaemon(t, home, env)
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(st.Socket), "mutation.lock"), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	client, err := daemon.Dial(context.Background(), st.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	daemonDone := make(chan error, 1)
	go func() {
		raw, err := client.Call(core.IDSessionStart, core.SessionStartIn{Session: "alpha"})
		if err == nil {
			var env core.Envelope
			err = json.Unmarshal(raw, &env)
			if err == nil && !env.OK {
				err = core.Errorf(env.Error.Code, "%s", env.Error.Message)
			}
		}
		daemonDone <- err
	}()
	writeCoreDaemonModeConfig(t, home, false)
	argv := exec.Command(channelsCLIBinary(t), "session", "start", "beta", "--json=envelope")
	argv.Env = agentDeckTestEnv(home, env)
	var argvOut bytes.Buffer
	argv.Stdout, argv.Stderr = &argvOut, &argvOut
	if err := argv.Start(); err != nil {
		t.Fatal(err)
	}
	argvDone := make(chan error, 1)
	go func() { argvDone <- argv.Wait() }()
	time.Sleep(300 * time.Millisecond)
	select {
	case err := <-daemonDone:
		t.Fatalf("daemon mutation bypassed lock: %v", err)
	default:
	}
	select {
	case err := <-argvDone:
		t.Fatalf("argv mutation bypassed lock: %v: %s", err, argvOut.String())
	default:
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	var order []string
	for len(order) < 2 {
		select {
		case err := <-daemonDone:
			if err != nil {
				t.Fatalf("daemon start: %v", err)
			}
			order = append(order, "alpha")
			daemonDone = nil
		case err := <-argvDone:
			if err != nil {
				t.Fatalf("argv start: %v: %s", err, argvOut.String())
			}
			order = append(order, "beta")
			argvDone = nil
		}
	}
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "list", "--json=envelope")
	if code != 0 || !strings.Contains(stdout, `"title": "alpha"`) || !strings.Contains(stdout, `"title": "beta"`) {
		t.Fatalf("raced store missing a session: exit %d: %s %s", code, stdout, stderr)
	}
	for _, name := range order {
		out, errOut, code := runAgentDeckEnv(t, serialHome, "", serialEnv, "session", "start", name, "--json=envelope")
		if code != 0 {
			t.Fatalf("serial start %s: exit %d: %s %s", name, code, out, errOut)
		}
	}
	if raced, serial := canonicalStateDB(t, home), canonicalStateDB(t, serialHome); !bytes.Equal(raced, serial) {
		diff := 0
		for diff < len(raced) && diff < len(serial) && raced[diff] == serial[diff] {
			diff++
		}
		t.Fatalf("raced storage bytes differ from serial execution (%d vs %d bytes, order %v, first offset %d, bytes %x vs %x)", len(raced), len(serial), order, diff, raced[diff:diff+16], serial[diff:diff+16])
	}
}

func canonicalStateDB(t *testing.T, home string) []byte {
	t.Helper()
	var path string
	for _, candidate := range []string{
		filepath.Join(home, ".agent-deck", "profiles", "ch_support_test", "state.db"),
		filepath.Join(home, ".local", "share", "agent-deck", "profiles", "ch_support_test", "state.db"),
	} {
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
			break
		}
	}
	if path == "" {
		t.Fatal("sandbox state.db not found")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(home, "canonical-state.db")
	if _, err := db.Exec("VACUUM INTO ?", out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDaemonDeadCLIStillWorks: with `[core] daemon = true` the CLI sends
// envelope requests to a live daemon, and keeps answering in process, with
// the same bytes, when the daemon was killed and left a stale socket.
func TestDaemonDeadCLIStillWorks(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	home := shortTempDir(t, "addd")
	tmuxDir := shortTempDir(t, "adddt")
	env := []string{"TMUX_TMPDIR=" + tmuxDir}
	t.Cleanup(func() { testutil.KillTmuxServersUnder(tmuxDir) })
	writeCoreDaemonConfig(t, home)
	seedSessions(t, home, env, "alpha")

	list := func(what string) string {
		t.Helper()
		stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "list", "--json=envelope")
		if code != 0 {
			t.Fatalf("%s: list --json=envelope exit %d\n%s\n%s", what, code, stdout, stderr)
		}
		return canonicalEnvelope(t, []byte(stdout))
	}
	direct := list("no daemon")

	cmd, st := startDaemon(t, home, env)
	if got := list("daemon alive"); got != direct {
		t.Errorf("envelope via daemon differs\ndirect: %s\ndaemon: %s", direct, got)
	}
	if after, _ := daemonStatus(t, home, env); after.Status == nil || after.Status.Calls != st.Status.Calls+1 {
		t.Fatalf("daemon served %+v calls after one CLI request, want %d", after.Status, st.Status.Calls+1)
	}

	// SIGKILL: no cleanup runs, the socket file stays behind.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(cmd.Process.Pid, 0) == nil && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Lstat(st.Socket); err != nil {
		t.Fatalf("killed daemon left no socket to recover from: %v", err)
	}
	dead, code := daemonStatus(t, home, env)
	if code == 0 || dead.State != string(daemon.StateStale) || dead.PID != cmd.Process.Pid {
		t.Fatalf("status after kill = %+v (exit %d), want stale pid %d", dead, code, cmd.Process.Pid)
	}

	if got := list("daemon dead"); got != direct {
		t.Errorf("envelope with a dead daemon differs\ndirect: %s\ngot:    %s", direct, got)
	}
	if stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "list"); code != 0 || !strings.Contains(stdout, "alpha") {
		t.Errorf("list with a dead daemon: exit %d\n%s\n%s", code, stdout, stderr)
	}
	stdout, _, code := runAgentDeckEnv(t, home, "", env, "session", "start", "nosuch", "--json=envelope")
	var bad core.Envelope
	if code != 2 || json.Unmarshal([]byte(stdout), &bad) != nil || bad.Error == nil || bad.Error.Code != core.CodeNotFound {
		t.Errorf("start nosuch with a dead daemon: exit %d\n%s", code, stdout)
	}

	// The next serve takes the stale socket over; stop shuts it down.
	next, _ := startDaemon(t, home, env)
	stdout, stderr, code := runAgentDeckEnv(t, home, "", env, "daemon", "stop")
	if code != 0 || !strings.Contains(stdout, strconv.Itoa(next.Process.Pid)) {
		t.Fatalf("daemon stop: exit %d\n%s\n%s", code, stdout, stderr)
	}
	if gone, code := daemonStatus(t, home, env); code == 0 || gone.State != string(daemon.StateAbsent) {
		t.Fatalf("status after stop = %+v (exit %d), want absent", gone, code)
	}
}

// TestDaemonSingleOwner: a second serve for the same profile is refused and
// leaves the first daemon serving; the socket is owner-only.
func TestDaemonSingleOwner(t *testing.T) {
	home := shortTempDir(t, "adso")
	first, st := startDaemon(t, home, nil)

	info, err := os.Stat(st.Socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %s mode %v, want socket 0600", st.Socket, info.Mode())
	}

	stdout, stderr, code := runAgentDeckEnv(t, home, "", nil, "daemon", "serve")
	want := "daemon already running (pid " + strconv.Itoa(first.Process.Pid) + ")"
	if code == 0 || !strings.Contains(stderr, want) {
		t.Fatalf("second serve: exit %d\nstdout %s\nstderr %s\nwant stderr containing %q", code, stdout, stderr, want)
	}
	if again, code := daemonStatus(t, home, nil); code != 0 || again.PID != first.Process.Pid {
		t.Fatalf("first daemon disturbed: %+v (exit %d)", again, code)
	}
}
