package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// #1873 reproduction.
//
// A session launched through a wrapper loses its tmux pane during the
// fast-start window while the wrapped process tree survives, reparented away
// from the pane. agent-deck records the session as errored — and the next
// legitimate restart spawns a SECOND tree against the same instance.
//
// The fixture below is the issue's own deterministic repository reproduction: a
// fake wrapper that starts a long-lived child in a new session/process group,
// makes it ignore SIGHUP, records the child's pid/pgid plus its
// /proc/<pid>/stat start time under the test's own temp dir, and then exits
// non-zero. Only pids this test created are ever signalled, on every exit path.

// escapedWrapper is a fake wrapper script plus the record of every child it has
// spawned across restarts.
type escapedWrapper struct {
	t      *testing.T
	script string
	recDir string
	linger time.Duration
	// beforePaneLoss, when set, runs in startEscapedInstance once the child
	// has published its identity and BEFORE the pane is allowed to die. It is
	// where a test synchronises on receipt state and then releases the
	// wrapper.
	beforePaneLoss func(inst *Instance, child escapedChild)
}

// release lets every wrapper that has published a child exit now, instead of
// at its linger bound. Wrappers are addressed by pid (the ready.<pid> marker),
// so releasing wave 1 does not pre-release a wave 2 wrapper started later.
func (w *escapedWrapper) release() {
	entries, _ := filepath.Glob(filepath.Join(w.recDir, "ready.*"))
	for _, path := range entries {
		pid := strings.TrimPrefix(filepath.Base(path), "ready.")
		_ = os.WriteFile(filepath.Join(w.recDir, "release."+pid), nil, 0o600)
	}
}

// fixtureOwnershipDir mirrors the production receipt directory resolution so a
// test can watch the receipt file directly, without depending on any symbol the
// fix adds (the same data-path helpers exist on pre-fix code).
func fixtureOwnershipDir() string {
	path, err := runtimeDataPath("ownership")
	if err != nil {
		return tempAgentDeckPath("runtime", "ownership")
	}
	return path
}

// receiptRecords reports whether the instance's on-disk receipt currently names
// pid. It reads the JSON generically: on pre-fix code there is no receipt and
// the answer is simply false.
func receiptRecords(instanceID string, pid int) bool {
	data, err := os.ReadFile(filepath.Join(fixtureOwnershipDir(), instanceID+".json"))
	if err != nil {
		return false
	}
	var doc struct {
		Leader struct {
			PID int `json:"pid"`
		} `json:"leader"`
		Members []struct {
			PID int `json:"pid"`
		} `json:"members"`
	}
	if json.Unmarshal(data, &doc) != nil {
		return false
	}
	if doc.Leader.PID == pid {
		return true
	}
	for _, m := range doc.Members {
		if m.PID == pid {
			return true
		}
	}
	return false
}

// waitForReceiptToRecord blocks until the receipt names the child or the
// timeout passes, and reports which. It never fails the test: the pre-fix
// branch of an acceptance test legitimately never sees a receipt.
func waitForReceiptToRecord(instanceID string, child escapedChild, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if receiptRecords(instanceID, child.PID) {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return receiptRecords(instanceID, child.PID)
}

// escapedChild is one recorded survivor.
type escapedChild struct {
	PID   int
	Start string
	PGID  int
}

func (c escapedChild) String() string {
	return fmt.Sprintf("pid=%d pgid=%d start=%s", c.PID, c.PGID, c.Start)
}

// requireEscapedWrapperSupport gates the fixture on what it actually needs: a
// Linux /proc for start identity and setsid(1) to detach the child. The
// ownership contract itself is cross-platform; this reproduction is not.
func requireEscapedWrapperSupport(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("#1873 reproduction needs /proc start identity (linux)")
	}
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid not available: cannot detach a child into its own session")
	}
	skipIfNoTmuxBinary(t)
}

// wrapperScript is the fixture. $$ inside the setsid'd shell is the child's own
// pid; fields 22 and 5 of /proc/<pid>/stat are its start time and its process
// group. The marker file is what lets the wrapper wait until the child has
// published its identity before dying, so the record is never half-written.
//
// After that the wrapper lingers until EITHER the test releases it (a
// release.<wrapper pid> file, see escapedWrapper.release) OR the linger bound
// elapses. The release is what makes a test deterministic: it can hold the
// wrapper alive until it has observed whatever state it needs — the receipt
// naming the child — instead of hoping a fixed sleep is long enough.
const wrapperScript = `#!/bin/sh
REC="$1"
LINGER="$2"
MARK="$REC/ready.$$"
RELEASE="$REC/release.$$"
setsid /bin/sh -c '
  trap "" HUP
  R="$1"; M="$2"
  set -- $(awk "{print \$22, \$5}" /proc/$$/stat)
  printf "%s %s %s\n" "$$" "$1" "$2" > "$R/child.$$.tmp"
  mv "$R/child.$$.tmp" "$R/child.$$.txt"
  : > "$M"
  while : ; do sleep 1; done
' fakeagent1873 "$REC" "$MARK" &
n=0
while [ ! -f "$MARK" ] && [ $n -lt 200 ]; do n=$((n+1)); sleep 0.05; done
# linger in 50ms steps, leaving early once released
steps=$(awk "BEGIN{printf \"%d\", $LINGER*20}")
n=0
while [ ! -f "$RELEASE" ] && [ $n -lt $steps ]; do n=$((n+1)); sleep 0.05; done
exit 3
`

// newEscapedWrapper writes the fixture and registers the cleanup that kills
// every pid it created — and nothing else — even when the test fails.
//
// linger is how long the wrapper stays alive after its child has published its
// identity. It is NOT cosmetic: a PID+start-time receipt can only record a
// descendant while that descendant is still reachable from the pane leader, so
// the wrapper has to outlive its own fork by at least one attribution pass.
// That is the honest boundary of this contract — see
// TestIssue1873_ChildThatDetachesBeforeAttributionIsNeverClaimed.
func newEscapedWrapper(t *testing.T, linger time.Duration) *escapedWrapper {
	t.Helper()
	dir := t.TempDir()
	recDir := filepath.Join(dir, "record")
	require.NoError(t, os.MkdirAll(recDir, 0o700))
	script := filepath.Join(dir, "wrapper-1873.sh")
	require.NoError(t, os.WriteFile(script, []byte(wrapperScript), 0o700))

	w := &escapedWrapper{t: t, script: script, recDir: recDir, linger: linger}
	t.Cleanup(w.killOwnPIDs)
	return w
}

// newSynchronisedEscapedWrapper is the fixture most tests want: the wrapper is
// held alive until the receipt names its child, then released. Attribution
// therefore cannot lose a scheduling race to the pane's death, and the tests
// built on it are deterministic rather than timed. The linger is only a bound
// for code that never writes a receipt.
func newSynchronisedEscapedWrapper(t *testing.T) *escapedWrapper {
	t.Helper()
	w := newEscapedWrapper(t, 15*time.Second)
	w.beforePaneLoss = func(inst *Instance, child escapedChild) {
		waitForReceiptToRecord(inst.ID, child, 10*time.Second)
		w.release()
	}
	return w
}

// command is the Instance.Command that runs the wrapper.
func (w *escapedWrapper) command() string {
	return fmt.Sprintf("%s %s %.2f", w.script, w.recDir, w.linger.Seconds())
}

// children returns every child the wrapper has recorded so far.
func (w *escapedWrapper) children() []escapedChild {
	entries, err := filepath.Glob(filepath.Join(w.recDir, "child.*.txt"))
	if err != nil {
		return nil
	}
	var out []escapedChild
	for _, path := range entries {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		parts := strings.Fields(string(data))
		if len(parts) != 3 {
			continue
		}
		pid, err1 := strconv.Atoi(parts[0])
		pgid, err2 := strconv.Atoi(parts[2])
		if err1 != nil || err2 != nil {
			continue
		}
		out = append(out, escapedChild{PID: pid, Start: parts[1], PGID: pgid})
	}
	return out
}

// waitForChildren blocks until the wrapper has recorded want children.
func (w *escapedWrapper) waitForChildren(want int, timeout time.Duration) []escapedChild {
	w.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if kids := w.children(); len(kids) >= want {
			return kids
		}
		time.Sleep(50 * time.Millisecond)
	}
	w.t.Fatalf("wrapper did not record %d child(ren) within %s (recorded %d)",
		want, timeout, len(w.children()))
	return nil
}

// killOwnPIDs terminates exactly the processes this fixture created, verifying
// each one's start identity first so a recycled pid is never signalled. It runs
// on every exit path, including failures and panics.
func (w *escapedWrapper) killOwnPIDs() {
	for _, child := range w.children() {
		if !childAlive(child) {
			continue
		}
		// The child is the leader of a process group it created for itself, so
		// its group contains only its own descendants.
		_ = syscall.Kill(-child.PGID, syscall.SIGTERM)
		if waitForChildGone(child, 2*time.Second) {
			continue
		}
		_ = syscall.Kill(-child.PGID, syscall.SIGKILL)
		if !waitForChildGone(child, 2*time.Second) {
			w.t.Errorf("fixture leaked a process it created: %s", child)
		}
	}
}

// childStart reads a live pid's start identity, or "" when it is gone.
func childStart(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return ""
	}
	line := string(data)
	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 {
		return ""
	}
	fields := strings.Fields(line[closeIdx+1:])
	if len(fields) <= 19 {
		return ""
	}
	if fields[0] == "Z" {
		return "" // exited, awaiting reaping: not alive
	}
	return fields[19]
}

// childAlive reports whether the recorded identity — pid AND start time — is
// still running. A pid whose start time has changed is somebody else.
func childAlive(child escapedChild) bool {
	return childStart(child.PID) == child.Start
}

func childPPID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return -1
	}
	line := string(data)
	closeIdx := strings.LastIndexByte(line, ')')
	if closeIdx < 0 {
		return -1
	}
	fields := strings.Fields(line[closeIdx+1:])
	if len(fields) < 2 {
		return -1
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return ppid
}

// fixturePanePID reads the pane's initial process id straight from tmux, the
// way paneAliveNow does, so this fixture depends on nothing the fix adds and
// the tests built on it compile — and fail — on pre-fix code.
func fixturePanePID(sess *tmux.Session) (int, error) {
	if sess == nil {
		return 0, fmt.Errorf("no tmux session")
	}
	args := []string{}
	if sock := strings.TrimSpace(sess.SocketName); sock != "" {
		args = append(args, "-L", sock)
	}
	args = append(args, "list-panes", "-t", sess.Name+":", "-F", "#{pane_pid}")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "tmux", args...).Output()
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("unparseable pane pid %q", strings.TrimSpace(string(out)))
	}
	return pid, nil
}

// liveWrappedTrees counts the wrapper's recorded children that are still alive
// under their recorded identity — the number of wrapped trees this instance
// currently has running.
func liveWrappedTrees(w *escapedWrapper) int {
	n := 0
	for _, c := range w.children() {
		if childAlive(c) {
			n++
		}
	}
	return n
}

func waitForChildGone(child escapedChild, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !childAlive(child) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	return !childAlive(child)
}

func requireChildAlive(t *testing.T, child escapedChild, msg string) {
	t.Helper()
	require.True(t, childAlive(child), "%s: %s", msg, child)
}

func requireChildGone(t *testing.T, child escapedChild, msg string) {
	t.Helper()
	require.True(t, waitForChildGone(child, 5*time.Second), "%s: %s", msg, child)
}

// stopEscapedInstance stops the instance and tolerates exactly one kind of
// error: tmux reporting that the session it was asked to kill is already gone.
// The pane died by construction in these tests, and Session.Kill can surface
// that as an error when its existence cache is briefly stale; the ownership
// reap runs before that return either way, and that reap is what these tests
// are about. Any other failure, or a session that is really still alive, fails
// the test.
func stopEscapedInstance(t *testing.T, inst *Instance, sync bool) {
	t.Helper()
	var err error
	if sync {
		err = inst.KillAndWait()
	} else {
		err = inst.Kill()
	}
	if err == nil {
		return
	}
	require.Contains(t, err.Error(), "failed to kill tmux session", "unexpected stop error: %v", err)
	require.False(t, paneAliveNow(inst.GetTmuxSession()),
		"stop reported a tmux failure while the pane is still alive: %v", err)
}

// startEscapedInstance starts an instance through the fake wrapper and waits
// for the pane to die while the wrapped child survives — the exact state the
// issue reports.
func startEscapedInstance(t *testing.T, w *escapedWrapper, id string) (*Instance, escapedChild) {
	t.Helper()
	inst := NewInstance(id, t.TempDir())
	// A tool with no ToolDef runs Command verbatim as the pane's initial
	// process, so the pane dies with the wrapper.
	inst.Tool = "customwrap1873"
	inst.Command = w.command()

	require.NoError(t, inst.Start())
	t.Cleanup(func() { _ = inst.Kill() })
	// A wrapper with no linger can take the pane down before this probe runs;
	// an unreadable pid is then expected, and the survivor is reparented by
	// construction. Any other probe failure is a broken fixture.
	panePID, paneErr := fixturePanePID(inst.GetTmuxSession())
	if paneErr != nil {
		require.False(t, paneAliveNow(inst.GetTmuxSession()),
			"pane pid unreadable while the pane is still alive: %v", paneErr)
	}

	kids := w.waitForChildren(1, 15*time.Second)
	child := kids[len(kids)-1]
	if w.beforePaneLoss != nil {
		w.beforePaneLoss(inst, child)
	}
	require.True(t, paneGoneWithin(inst, 20*time.Second),
		"the pane must die inside the fast-death window for this reproduction")
	requireChildAlive(t, child, "the wrapped child must survive the pane it was launched from")
	ppid := childPPID(child.PID)
	require.Greater(t, ppid, 0, "the survivor must still have a readable parent")
	if paneErr == nil {
		require.NotEqual(t, panePID, ppid,
			"the survivor must have been reparented away from the pane tree")
	}
	return inst, child
}

// TestIssue1873_RestartIsNotAdmittedWhileTheOwnedTreeSurvives is the
// behavioural core of the report, written against the public seams only so it
// runs unchanged on pre-fix code — where it fails, because the restart is
// admitted and a second tree appears alongside the survivor.
func TestIssue1873_RestartIsNotAdmittedWhileTheOwnedTreeSurvives(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newSynchronisedEscapedWrapper(t)
	inst, survivor := startEscapedInstance(t, w, "test-1873-escaped")

	err := inst.Restart()

	require.Error(t, err,
		"restart must not be admitted while an identity-matched owned process is still alive")
	assert.Contains(t, strings.ToLower(err.Error()), "refused")
	requireChildAlive(t, survivor, "a refused restart must not signal the survivor")
	assert.False(t, paneAliveNow(inst.GetTmuxSession()),
		"no replacement pane may exist: that is the second tree this issue is about")
	assert.Len(t, w.children(), 1,
		"exactly one wrapped tree may exist for this instance")
}

// TestIssue1873_TeardownReapsTheEscapedTree proves the survivor is reachable
// after the fact: a deliberate stop reaps what the receipt owns, even though
// the pane it escaped from is long gone.
func TestIssue1873_TeardownReapsTheEscapedTree(t *testing.T) {
	requireEscapedWrapperSupport(t)

	w := newSynchronisedEscapedWrapper(t)
	inst, survivor := startEscapedInstance(t, w, "test-1873-teardown")

	stopEscapedInstance(t, inst, false)
	requireChildGone(t, survivor, "a deliberate stop must reap the escaped tree")
}
