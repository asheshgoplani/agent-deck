//sixgate:lint-allow-tmux-invocation — THIS FILE IS THE CHOKEPOINT.
//
// Every tmux invocation SIXGATE will ever make is built here, by one function,
// from one program-name constant, with one socket address and one pinned
// environment. `sixgate selfcheck` enforces exactly that: this is the only file
// in internal/sixgate or tools/sixgate permitted to name the tmux program, it
// must carry this marker on line one, and it must contain exactly one
// exec.Command call and exactly one program-name literal. Every other file in
// the framework is forbidden from naming tmux at all.
//
// That is not stylistic. The rules encoded below were each written by an
// incident on this machine:
//
//   - 2026-04-17. A harness set TMUX_TMPDIR but left $TMUX set. tmux resolves
//     $TMUX before -S, before -L, before $TMUX_TMPDIR, so the isolation was
//     decorative and the harness joined the developer's live server. Fixed by
//     testutil.IsolateTmuxSocket, which unsets TMUX/TMUX_PANE FIRST. This file
//     refuses to start if either is set afterwards.
//
//   - 2026-07-18. Teardown addressed a socket the spawn had not used, because
//     the child's environment had been stripped of TMUX_TMPDIR while cleanup
//     kept it. kill-server became a silent no-op; ~50 orphaned servers held
//     507 of the host's 511 pseudo-terminals and nothing on the machine could
//     attach. Here the environment is PINNED on every invocation — teardown
//     cannot drift from spawn because they are the same three lines of code —
//     and teardown's error is recorded in run.json, never discarded.
//
//   - 2026-07-26. A reaper selected processes by matching their argv against a
//     fixed pattern. On macOS a process keeps its original argv, so a server
//     auto-started by a client carries the client's argv too — and the reaper
//     killed the main server, taking the entire live session fleet with it.
//     Nothing here identifies a tmux process by name or argv. Identity is the
//     socket path, only ever the socket path, and the framework-wide lint fails
//     on every process-name matcher — including inside this file, which is
//     exempt from naming tmux but NOT from that rule.
//
// Selfcheck enforces the last point literally, which is why the narrative above
// describes those matchers instead of spelling them: the guard is armed on this
// file, and an incident note that trips it is a note that would have to be
// silenced to be written.
//
// The postcondition is the part that matters most: after teardown, `ls` on this
// run's own socket must FAIL, and no socket file may remain anywhere under the
// isolated directory. A harness that merely promises to clean up is how the
// fleet died twice. This one has to prove it, in writing, every run.

package panedrive

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// tmuxBin is the only occurrence of the tmux program name in the framework, and
// selfcheck asserts that. Routing every invocation through one constant is what
// makes "all tmux traffic goes through one chokepoint" a checkable property
// rather than a habit.
const tmuxBin = "tmux"

// socketPrefix names this framework's sockets. A run's socket is
// ctxgate-<runID>: dedicated, deterministic, and never the default socket or
// the user's.
const socketPrefix = "ctxgate-"

// sunPathLimit is the shortest sockaddr_un.sun_path any supported platform
// offers (104 on darwin, 108 on linux). A socket path that overshoots it fails
// at bind time with a message about file names, which reads like anything but
// the truth, so the length is checked up front with room to spare.
const sunPathLimit = 100

// cmdTimeout bounds a single tmux client invocation. tmux commands are local
// and instant; a hang means the server is wedged, and blocking a gate forever
// on that is worse than failing it.
const cmdTimeout = 20 * time.Second

// teardownSettle is how long the postcondition is allowed to become true after
// the server has been told to die. A server unlinks its socket while exiting,
// which is a moment after the command that told it returns.
const teardownSettle = 5 * time.Second

// ctl is the tmux control surface: one run, one socket, one pinned environment.
type ctl struct {
	// runID is the 8-hex identifier the whole run is keyed by.
	runID string
	// socketName is the -L name: ctxgate-<runID>.
	socketName string
	// tmpDir is the isolated TMUX_TMPDIR testutil handed us. Every socket this
	// run can possibly create lives under it, which is what makes a single
	// teardown sweep sufficient.
	tmpDir string
	// resolved is where tmux will place the -L socket:
	// <tmpDir>/tmux-<uid>/<socketName>. Recorded so an artifact states the
	// exact path rather than a name that has to be resolved to be checked.
	resolved string
	// baseEnv is the environment handed to EVERY invocation: the process
	// environment with every TMUX* variable removed, then TMUX_TMPDIR pinned
	// back to tmpDir. Pinning is the fix for the 2026-07-18 drift.
	baseEnv []string
	// release is testutil.IsolateTmuxSocket's cleanup: it kills every server
	// under tmpDir by absolute socket path, then removes the directory.
	release func()
	// spawned records whether a server was ever started on our socket, so the
	// teardown report can distinguish "killed it" from "there was never one".
	spawned bool
}

// isolationReport is the refuse-to-start evidence, recorded in run.json so a
// reader can see WHICH guards were checked rather than trusting that they were.
type isolationReport struct {
	RunID        string `json:"run_id"`
	SocketName   string `json:"socket_name"`
	SocketPath   string `json:"socket_path"`
	TmuxTmpdir   string `json:"tmux_tmpdir"`
	TmuxVar      string `json:"tmux_env_var"`
	TmuxPaneVar  string `json:"tmux_pane_env_var"`
	IsolatedMark string `json:"agent_deck_test_isolated"`
	TmuxVersion  string `json:"tmux_version"`
	Checks       []struct {
		Name string `json:"name"`
		OK   bool   `json:"ok"`
		Note string `json:"note,omitempty"`
	} `json:"checks"`
}

// teardownReport is the proof, not the promise.
type teardownReport struct {
	// ServerWasSpawned says whether there was anything to kill.
	ServerWasSpawned bool `json:"server_was_spawned"`
	// KillAddress is the socket this run's own kill was addressed to.
	KillAddress string `json:"kill_address"`
	// KillError is our own kill's error, recorded rather than discarded. The
	// 2026-07-18 incident's teardown discarded exactly this value with `_ =`.
	KillError string `json:"kill_error,omitempty"`
	// KillStderr is what tmux said. "no server running" here is a pass, not a
	// failure: it means the pane already exited.
	KillStderr string `json:"kill_stderr,omitempty"`
	// SweepDir is the directory testutil.KillTmuxServersUnder swept by absolute
	// socket path with a TMUX*-scrubbed environment.
	SweepDir string `json:"sweep_dir"`
	// SweepKilled lists socket paths that existed when the sweep ran. It covers
	// servers this framework did not create — notably the DEFAULT-socket server
	// the agent-deck binary under test may start for its own sessions, which
	// lands under our TMUX_TMPDIR precisely so this sweep reaches it.
	SweepKilled []string `json:"sweep_killed,omitempty"`
	// Residual is every socket file still present after the sweep, each with the
	// only fact that matters: whether a server still answers on it.
	Residual []residualSocket `json:"residual,omitempty"`
	// PostconditionCmd is the verification actually run.
	PostconditionCmd string `json:"postcondition_cmd"`
	// PostconditionFailed must be TRUE. `ls` succeeding means a server is still
	// alive on our socket, which is the fleet-killer condition.
	PostconditionFailed bool `json:"postcondition_ls_failed"`
	// PostconditionOutput is tmux's answer, quoted.
	PostconditionOutput string `json:"postcondition_output,omitempty"`
	// LiveSockets are the residual sockets a server still answers on. ANY entry
	// is a leak and fails the gate.
	LiveSockets []string `json:"live_sockets,omitempty"`
	// StaleSocketFiles are residual socket files with no server behind them.
	// They are recorded but are NOT a leak: a dying tmux server unlinks its
	// socket a moment after the command that killed it returns, and the file
	// holds no process and no pseudo-terminal in the meantime. The isolated
	// directory is removed straight after this check, which is safe precisely
	// because liveness was proved first — unlinking a socket whose server is
	// still running is what strands a server forever.
	StaleSocketFiles []string `json:"stale_socket_files,omitempty"`
	// Verified is the single boolean the gate reads: no server answers on this
	// run's own socket, and none answers on any socket left under its directory.
	Verified bool `json:"verified"`
}

// residualSocket is one socket file found after the sweep, with the only fact
// that decides whether it matters.
type residualSocket struct {
	Path string `json:"path"`
	// Alive is what an addressed query answered. A socket FILE is not a leak; a
	// server is. Distinguishing them is what keeps this gate strict without
	// making it cry wolf — and a safety gate that cries wolf is a safety gate
	// somebody switches off, which is how this machine lost its fleet.
	Alive bool `json:"server_alive"`
}

// newCtl isolates the process, pins the environment and REFUSES TO START unless
// every isolation guard holds.
//
// Refusing is the whole point. A driver that starts anyway and hopes is how a
// harness ends up on the user's live server: every failure mode in this file's
// header began with something continuing past a condition it should have
// stopped on.
func newCtl(runID string) (*ctl, isolationReport, error) {
	rep := isolationReport{RunID: runID, SocketName: socketPrefix + runID}
	add := func(name string, ok bool, note string) {
		rep.Checks = append(rep.Checks, struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
			Note string `json:"note,omitempty"`
		}{name, ok, note})
	}

	if _, err := exec.LookPath(tmuxBin); err != nil {
		add("tmux is installed", false, err.Error())
		return nil, rep, fmt.Errorf("panedrive: tmux is not on PATH, so there is no pane to drive: %w", err)
	}
	add("tmux is installed", true, "")

	// Isolation comes FIRST: it unsets TMUX and TMUX_PANE before anything can
	// resolve a socket, and panics rather than hand back a TMUX_TMPDIR that
	// points at tmux's default base.
	release := testutil.IsolateTmuxSocket()
	fail := func(err error) (*ctl, isolationReport, error) {
		release()
		return nil, rep, err
	}

	// TEMPORARY GUARD DEMONSTRATION — reverted immediately after.
	// Re-set TMUX after isolation to recreate the 2026-04-17 condition exactly.
	// The value points at a socket path that does not exist, so if the guard
	// FAILED to fire, tmux would error out rather than reach the live fleet.
	_ = os.Setenv("TMUX", "/private/tmp/ad-tmux-nonexistent-guard-demo/s,99999,0")

	c := &ctl{runID: runID, socketName: rep.SocketName, release: release}
	c.tmpDir = os.Getenv("TMUX_TMPDIR")
	c.resolved = filepath.Join(c.tmpDir, fmt.Sprintf("tmux-%d", os.Getuid()), c.socketName)
	rep.TmuxTmpdir = c.tmpDir
	rep.SocketPath = c.resolved
	rep.TmuxVar = os.Getenv("TMUX")
	rep.TmuxPaneVar = os.Getenv("TMUX_PANE")
	rep.IsolatedMark = os.Getenv(testutil.TestIsolationMarkerEnv)

	// 1. $TMUX beats every other address in tmux's discovery order. If it
	//    survived isolation, nothing below this line means anything.
	if rep.TmuxVar != "" || rep.TmuxPaneVar != "" {
		add("TMUX and TMUX_PANE are unset", false, fmt.Sprintf("TMUX=%q TMUX_PANE=%q", rep.TmuxVar, rep.TmuxPaneVar))
		return fail(fmt.Errorf(
			"panedrive: refusing to start — $TMUX=%q survived isolation. tmux resolves $TMUX before -L, "+
				"so this run would have joined the live server instead of its own (2026-04-17)", rep.TmuxVar))
	}
	add("TMUX and TMUX_PANE are unset", true, "")

	// 2. The isolation marker proves testutil ran, not that someone set
	//    TMUX_TMPDIR by hand to a directory that happens to look isolated.
	if rep.IsolatedMark != "1" {
		add("isolation marker set by testutil", false, fmt.Sprintf("%s=%q", testutil.TestIsolationMarkerEnv, rep.IsolatedMark))
		return fail(fmt.Errorf("panedrive: refusing to start — %s is not set, so isolation did not happen", testutil.TestIsolationMarkerEnv))
	}
	add("isolation marker set by testutil", true, "")

	// 3. The isolated directory must be a private one this run owns. tmux nests
	//    <TMUX_TMPDIR>/tmux-<uid>/<name>, so a tmp ROOT is the default base and
	//    a tmux-<uid> directory is the default base one level in. Landing on
	//    either would make the sweep in teardown hostile: it would kill the
	//    user's live servers.
	if err := requirePrivateTmpdir(c.tmpDir); err != nil {
		add("TMUX_TMPDIR is a private ad-tmux-* directory", false, err.Error())
		return fail(fmt.Errorf("panedrive: refusing to start — %w", err))
	}
	add("TMUX_TMPDIR is a private ad-tmux-* directory", true, c.tmpDir)

	// 4. The socket this run will use must be inside that private directory.
	//    This is the "foreign socket" guard: it is what stops a mis-set
	//    TMUX_TMPDIR from pointing our -L name at somebody else's server.
	if !withinDir(c.tmpDir, c.resolved) {
		add("socket resolves inside the isolated directory", false, c.resolved)
		return fail(fmt.Errorf("panedrive: refusing to start — socket %q is not under the isolated dir %q; that is a foreign socket", c.resolved, c.tmpDir))
	}
	add("socket resolves inside the isolated directory", true, c.resolved)

	// 5. sun_path is 104 bytes on darwin. Overshooting produces "File name too
	//    long" at bind time, which reads like a bug in the harness rather than
	//    in the path — see testutil.ShortTmuxSocket, which exists for this. The
	//    isolated base is already placed under /tmp for the same reason; this
	//    asserts the outcome instead of assuming it.
	if len(c.resolved) >= sunPathLimit {
		add("socket path fits sun_path", false, fmt.Sprintf("%d bytes", len(c.resolved)))
		return fail(fmt.Errorf("panedrive: refusing to start — socket path is %d bytes, over the %d-byte sun_path budget: %s",
			len(c.resolved), sunPathLimit, c.resolved))
	}
	add("socket path fits sun_path", true, fmt.Sprintf("%d bytes", len(c.resolved)))

	// 6. Nothing may already be listening on our address. A pre-existing socket
	//    means either a colliding run or a stale server, and attaching to
	//    either would put this gate's keystrokes into somebody else's pane.
	if _, err := os.Stat(c.resolved); err == nil {
		add("socket does not already exist", false, "a socket is already present at this path")
		return fail(fmt.Errorf("panedrive: refusing to start — a socket already exists at %s; this run would have joined a server it did not create", c.resolved))
	} else if !errors.Is(err, os.ErrNotExist) {
		add("socket does not already exist", false, err.Error())
		return fail(fmt.Errorf("panedrive: refusing to start — cannot stat %s: %w", c.resolved, err))
	}
	add("socket does not already exist", true, "")

	c.baseEnv = pinnedEnv(c.tmpDir, nil)
	rep.TmuxVersion = c.version()
	return c, rep, nil
}

// requirePrivateTmpdir rejects any directory that is, or sits at, tmux's own
// default socket base. It mirrors testutil's internal assertion because the
// guard has to be checkable from here too: a driver that trusts an invariant it
// cannot see is a driver that stops holding it the day the invariant moves.
func requirePrivateTmpdir(dir string) error {
	if strings.TrimSpace(dir) == "" {
		return errors.New("TMUX_TMPDIR is empty, so tmux would fall back to its default socket base")
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("TMUX_TMPDIR %q is not absolute", dir)
	}
	clean := filepath.Clean(dir)
	for _, base := range []string{"/tmp", "/private/tmp", filepath.Clean(os.TempDir())} {
		if clean == base {
			return fmt.Errorf("TMUX_TMPDIR %q IS tmux's default socket base; teardown would sweep the user's live servers", clean)
		}
	}
	if strings.HasPrefix(filepath.Base(clean), "tmux-") {
		return fmt.Errorf("TMUX_TMPDIR %q is tmux's per-uid socket directory, not a private base", clean)
	}
	if !strings.HasPrefix(filepath.Base(clean), "ad-tmux-") {
		return fmt.Errorf("TMUX_TMPDIR %q was not created by testutil.IsolateTmuxSocket (expected an ad-tmux-* directory)", clean)
	}
	return nil
}

// withinDir reports whether path is dir or lives beneath it.
func withinDir(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// pinnedEnv builds the environment for an invocation: the process environment
// with every TMUX* variable dropped, then TMUX_TMPDIR pinned back, then the
// caller's additions.
//
// Dropping and re-pinning rather than inheriting is the fix for the 2026-07-18
// drift: spawn and teardown cannot disagree about where the socket is, because
// neither of them is reading a value anybody else can change.
func pinnedEnv(tmpDir string, extra map[string]string) []string {
	out := make([]string, 0, len(os.Environ())+len(extra)+1)
	overridden := make(map[string]bool, len(extra))
	for k := range extra {
		overridden[k] = true
	}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "TMUX") || overridden[key] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "TMUX_TMPDIR="+tmpDir)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+extra[k])
	}
	return out
}

// ownAddr is this run's dedicated socket, by name, resolved against the pinned
// TMUX_TMPDIR. It is the address of every command except the liveness probes in
// teardown, which name a discovered socket absolutely.
func (c *ctl) ownAddr() []string { return []string{"-L", c.socketName} }

// socketAddr addresses one specific socket file. Absolute paths cannot drift
// with the environment, which is why teardown's proof uses this form.
func socketAddr(path string) []string { return []string{"-S", path} }

// cmd is the single exec point. Selfcheck asserts this file contains exactly one
// exec call, and the panic below asserts at runtime what the lint asserts at
// build time: every invocation names a socket. Between them, "no unaddressed
// tmux command can exist in this framework" stops being a convention and becomes
// arithmetic — there is exactly one place such a command could be built, and it
// refuses to build one.
func (c *ctl) cmd(addr, env []string, args ...string) *exec.Cmd {
	if len(addr) != 2 || (addr[0] != "-L" && addr[0] != "-S") {
		panic(fmt.Sprintf("panedrive: refusing to build an unaddressed tmux command (%v). "+
			"An unaddressed command reaches the DEFAULT socket, which on this host is the live session fleet.", addr))
	}
	full := append(append([]string{}, addr...), args...)
	cmd := exec.Command(tmuxBin, full...) //nolint:gosec // fixed program, address asserted above
	if env == nil {
		env = c.baseEnv
	}
	cmd.Env = env
	return cmd
}

// run executes one command against this run's own socket.
func (c *ctl) run(args ...string) (string, string, error) {
	return c.runEnv(c.ownAddr(), nil, args...)
}

// runEnv executes one addressed tmux command with an optional environment
// override. Only the server-creating invocation needs one: the tmux server
// inherits the environment of the client that starts it, and every pane
// inherits the server.
func (c *ctl) runEnv(addr, env []string, args ...string) (stdout, stderr string, err error) {
	cmd := c.cmd(addr, env, args...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err = <-done:
	case <-time.After(cmdTimeout):
		_ = cmd.Process.Kill()
		<-done
		err = fmt.Errorf("tmux %s timed out after %s", strings.Join(args, " "), cmdTimeout)
	}
	return so.String(), strings.TrimSpace(se.String()), err
}

// version records which tmux produced the artifacts. Frame content depends on
// it, so an artifact that does not say is an artifact that cannot be reproduced.
func (c *ctl) version() string {
	out, _, err := c.run("-V")
	if err != nil {
		return "unknown"
	}
	return strings.TrimSpace(out)
}

// newSession starts the server and the pane in one invocation, so the server's
// environment is exactly the world's environment and no earlier command can
// have created a server with a different one.
func (c *ctl) newSession(name string, width, height int, worldEnv map[string]string, argv ...string) error {
	args := []string{
		"new-session", "-d",
		"-s", name,
		"-x", fmt.Sprintf("%d", width),
		"-y", fmt.Sprintf("%d", height),
	}
	args = append(args, argv...)
	_, stderr, err := c.runEnv(c.ownAddr(), pinnedEnv(c.tmpDir, worldEnv), args...)
	if err != nil {
		return fmt.Errorf("starting the pane: %w (%s)", err, stderr)
	}
	c.spawned = true
	return nil
}

// capture returns what the pane is painting. With ansi set it keeps the escape
// sequences, which is the evidence for colour and bleed that a stripped frame
// cannot carry.
func (c *ctl) capture(target string, ansi bool) (string, error) {
	args := []string{"capture-pane", "-p", "-t", target}
	if ansi {
		args = append(args, "-e")
	}
	out, stderr, err := c.run(args...)
	if err != nil {
		return "", fmt.Errorf("capturing the pane: %w (%s)", err, stderr)
	}
	return out, nil
}

// sendNamed sends a tmux key name (Enter, Escape, Down…).
func (c *ctl) sendNamed(target, key string) error {
	_, stderr, err := c.run("send-keys", "-t", target, key)
	if err != nil {
		return fmt.Errorf("sending %s: %w (%s)", key, err, stderr)
	}
	return nil
}

// sendLiteral sends text as literal characters, never as key names.
//
// Without -l, tmux interprets its argument as a key name first, so a script
// asking for the letter "C" is one lookup away from becoming something else.
// Producing a confident transcript of the wrong keystroke is precisely the
// failure this framework exists to prevent.
func (c *ctl) sendLiteral(target, text string) error {
	_, stderr, err := c.run("send-keys", "-t", target, "-l", "--", text)
	if err != nil {
		return fmt.Errorf("sending literal %q: %w (%s)", text, err, stderr)
	}
	return nil
}

// alive reports whether a server is answering on OUR socket.
func (c *ctl) alive() bool {
	_, _, err := c.run("ls")
	return err == nil
}

// hasSession reports whether the named session is still on our socket, which is
// how the driver notices the TUI exited.
func (c *ctl) hasSession(name string) bool {
	_, _, err := c.run("has-session", "-t", name)
	return err == nil
}

// teardown kills the server, sweeps the isolated directory, and then PROVES
// both worked. It is the most important function in the driver.
//
// Order is load-bearing and each step is here because skipping it once cost
// this machine its entire session fleet:
//
//  1. Kill our own server at our own address, and RECORD the error. The
//     2026-07-18 teardown discarded it and could not tell a successful kill
//     from a silent no-op.
//  2. Sweep every socket under the isolated directory with
//     testutil.KillTmuxServersUnder, which addresses by absolute -S path with a
//     TMUX*-scrubbed environment. This catches servers this framework did not
//     create — the agent-deck binary under test starts its own sessions on
//     tmux's DEFAULT socket, which lands under our TMUX_TMPDIR exactly so this
//     sweep reaches it.
//  3. Verify: `ls` on our socket must FAIL and no socket file may remain. A
//     server whose socket was unlinked does not die; it keeps its panes, keeps
//     a pseudo-terminal each, and becomes permanently unreachable. So the check
//     runs BEFORE anything is removed.
//  4. Only then release the isolation, which kills again and removes the dir.
func (c *ctl) teardown() teardownReport {
	rep := teardownReport{
		ServerWasSpawned: c.spawned,
		KillAddress:      c.resolved,
		SweepDir:         c.tmpDir,
		PostconditionCmd: fmt.Sprintf("%s -L %s ls   (must fail)", tmuxBin, c.socketName),
	}

	// 1. Our own kill, at our own address, with its error kept.
	_, stderr, err := c.run("kill-server")
	if err != nil {
		rep.KillError = err.Error()
	}
	rep.KillStderr = stderr

	// 2. The sweep. Record what was there first: KillTmuxServersUnder is
	//    deliberately best-effort and returns nothing, so the census taken
	//    immediately before it is the only way an artifact can say what it
	//    acted on.
	rep.SweepKilled = socketsUnder(c.tmpDir)
	testutil.KillTmuxServersUnder(c.tmpDir)

	// 3. The postcondition, before any directory is removed.
	//
	//    Both halves are polled rather than sampled once, because a server that
	//    has been told to die unlinks its socket as it exits, a moment after the
	//    command that told it returns. Sampling immediately reports a socket
	//    that is already doomed and calls it a leak — and a safety gate that
	//    cries wolf is a safety gate somebody switches off, which is how this
	//    machine lost its fleet. Polling keeps the check strict: anything still
	//    present when the window closes is a genuine finding, and the window is
	//    short enough that a live server cannot hide inside it.
	deadline := time.Now().Add(teardownSettle)
	for {
		out, lsErr, lsFailed := c.lsMustFail()
		rep.PostconditionFailed = lsFailed
		rep.PostconditionOutput = strings.TrimSpace(out + " " + lsErr)
		rep.Residual = c.probeResidual(c.tmpDir)
		if rep.PostconditionFailed && !anyAlive(rep.Residual) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	rep.LiveSockets, rep.StaleSocketFiles = splitResidual(rep.Residual)
	rep.Verified = rep.PostconditionFailed && len(rep.LiveSockets) == 0

	// 4. Release the isolation last: it kills by absolute socket path again and
	//    then removes the directory the postcondition just inspected.
	if c.release != nil {
		c.release()
		c.release = nil
	}
	return rep
}

// lsMustFail runs the postcondition and reports whether it failed, which is the
// passing outcome. It is a named function so the inversion is stated once, in
// one place, rather than re-derived at each call site.
func (c *ctl) lsMustFail() (stdout, stderr string, failed bool) {
	stdout, stderr, err := c.run("ls")
	return stdout, stderr, err != nil
}

// probeResidual lists every socket file left under dir and asks each one,
// absolutely addressed, whether a server is still answering.
//
// Asking is the whole difference between a leak and a leftover. `ls` on a path
// with no server prints "no server running" and exits non-zero; it never starts
// one, because listing sessions is not a session-creating command. So the probe
// is read-only in the only sense that matters: it cannot bring a server into
// existence, and it identifies by socket path rather than by anything about a
// process.
func (c *ctl) probeResidual(dir string) []residualSocket {
	paths := socketsUnder(dir)
	out := make([]residualSocket, 0, len(paths))
	for _, p := range paths {
		_, _, err := c.runEnv(socketAddr(p), nil, "ls")
		out = append(out, residualSocket{Path: p, Alive: err == nil})
	}
	return out
}

func anyAlive(rs []residualSocket) bool {
	for _, r := range rs {
		if r.Alive {
			return true
		}
	}
	return false
}

func splitResidual(rs []residualSocket) (live, stale []string) {
	for _, r := range rs {
		if r.Alive {
			live = append(live, r.Path)
			continue
		}
		stale = append(stale, r.Path)
	}
	return live, stale
}

// socketsUnder lists socket files anywhere beneath dir.
//
// It is a read-only directory walk. It identifies nothing by process name and
// terminates nothing; the paths it returns are handed to a killer that
// addresses them absolutely. Socket paths are identity here because on macOS a
// process keeps its original argv and argv therefore is not.
func socketsUnder(dir string) []string {
	if strings.TrimSpace(dir) == "" {
		return nil
	}
	var out []string
	patterns := []string{
		filepath.Join(dir, "*"),
		filepath.Join(dir, "tmux-*", "*"),
	}
	seen := map[string]bool{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			info, err := os.Stat(m)
			if err != nil || info.Mode()&os.ModeSocket == 0 {
				continue
			}
			out = append(out, m)
		}
	}
	sort.Strings(out)
	return out
}
