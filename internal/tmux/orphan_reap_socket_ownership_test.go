package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ownership boundary this file exists for.
//
// isLiveTmuxClientOrServer takes the candidate's socket SELECTOR from the
// candidate's own argv (-L work), but the query it then issues resolves that
// name through THIS process's TMUX_TMPDIR. Two tmux servers can carry the same
// -L name under different bases, and they are different servers. When that
// happens both queries succeed against the wrong one, neither its server pid
// nor any of its client pids matches, and the function answers live=false,
// ok=true — the kill verdict — against a client that is attached and alive on
// its own server.
//
// Every pre-existing case in orphan_reap_live_identity_test.go creates the
// server with `-L <socket>` and puts that same `-L <socket>` in the candidate
// argv, so the candidate's socket always equals the resolved socket and this
// case cannot arise. A case the suite never constructs cannot fail it, which is
// why green CI never said anything about this.

// tmuxUnder runs a raw tmux command under an explicit TMUX_TMPDIR, so a test can
// stand up a server that this process does NOT resolve to.
//
// Deliberately not tmuxExec: the package factory resolves -L through the test
// process's own env, which is the very coupling under test here.
func tmuxUnder(t *testing.T, tmpdir string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("tmux", args...)
	cmd.Env = append(os.Environ(), "TMUX_TMPDIR="+tmpdir, "TMUX=")
	return cmd
}

// foreignTmuxBase makes a TMUX_TMPDIR that is not this process's, and keeps it
// short: the socket path lands in a sockaddr_un, which is capped near 104 bytes,
// and t.TempDir() names itself after the test.
func foreignTmuxBase(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "adfgn")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// startServerUnder creates a session on socketName under base, with exit-empty
// off so the server outlives its own command, and registers teardown that kills
// it through the SAME env — a mismatch there makes kill-server a silent no-op
// and strands a server plus its ptys.
func startServerUnder(t *testing.T, base, socketName, session string) {
	t.Helper()
	out, err := tmuxUnder(t, base, "-L", socketName, "new-session", "-d", "-s", session, "sleep 6000").CombinedOutput()
	require.NoError(t, err, "new-session under %s: %s", base, out)
	t.Cleanup(func() { _ = tmuxUnder(t, base, "-L", socketName, "kill-server").Run() })
	out, err = tmuxUnder(t, base, "-L", socketName, "set-option", "-g", "exit-empty", "off").CombinedOutput()
	require.NoError(t, err, "exit-empty off: %s", out)
}

// attachedClientUnder spawns a control-mode client attached to a session on
// base's server, reparented away from the test binary, and returns its pid once
// the server has registered it. It is a real, live, attached client — the class
// isLiveTmuxClientOrServer must never report as reapable.
func attachedClientUnder(t *testing.T, base, socketName, session string) int {
	t.Helper()
	dir := t.TempDir()
	fifoPath := filepath.Join(dir, "stdin.fifo")
	require.NoError(t, syscall.Mkfifo(fifoPath, 0o600))
	keepOpen, err := os.OpenFile(fifoPath, os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = keepOpen.Close() })

	pidPath := filepath.Join(dir, "pid")
	logPath := filepath.Join(dir, "out.log")
	argv := []string{"tmux", "-L", socketName, "-C", "attach-session", "-t", session}
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shQuote(a)
	}
	script := "TMUX= TMUX_TMPDIR=" + shQuote(base) + " " + strings.Join(quoted, " ") +
		" < " + shQuote(fifoPath) + " > " + shQuote(logPath) + " 2>&1 & echo $! > " + shQuote(pidPath) + "; exit 0"

	wrapper := exec.Command("sh", "-c", script)
	out, err := wrapper.CombinedOutput()
	require.NoError(t, err, "sh wrapper: %s", out)

	var pidBytes []byte
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		pidBytes, err = os.ReadFile(pidPath)
		if err == nil && strings.TrimSpace(string(pidBytes)) != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	require.NoError(t, err, "read pid file")
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// Registration happens asynchronously after exec, so wait for the server to
	// actually know about it — otherwise a refusal could be a timing artefact
	// rather than the boundary under test.
	deadline = time.Now().Add(5 * time.Second)
	target := strconv.Itoa(pid)
	for time.Now().Before(deadline) {
		out, err := tmuxUnder(t, base, "-L", socketName, "list-clients", "-F", "#{client_pid}").Output()
		if err == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
				if strings.TrimSpace(line) == target {
					return pid
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	logContents, _ := os.ReadFile(logPath)
	t.Fatalf("client pid %d never registered on %s/%s (log: %s)", pid, base, socketName, logContents)
	return 0
}

// TestIsLiveTmuxClientOrServer_RefusesACandidateOnAnotherServer is the case the
// suite could not construct. Two servers carry the same -L name under different
// bases. The candidate is attached and alive on the foreign one; the query
// resolves to ours, which is also running and answering — so nothing fails, and
// the honest answer ("I asked the wrong server") is only available by comparing
// sockets.
func TestIsLiveTmuxClientOrServer_RefusesACandidateOnAnotherServer(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resolving a candidate's own socket reads /proc/<pid>/environ; linux-only by construction")
	}
	const socketName = "adwrongsrv"
	const session = "agentdeck_wrong_server"

	// Ours: a live server on the same -L name, under the package's isolated
	// TMUX_TMPDIR. It must exist and answer, or the refusal would come from
	// "no server there" and prove nothing about the boundary.
	ourBase := os.Getenv("TMUX_TMPDIR")
	require.NotEmpty(t, ourBase, "package TestMain must have isolated TMUX_TMPDIR")
	startServerUnder(t, ourBase, socketName, "agentdeck_ours")

	// Theirs: same name, different base, with a live attached client.
	foreign := foreignTmuxBase(t)
	startServerUnder(t, foreign, socketName, session)
	pid := attachedClientUnder(t, foreign, socketName, session)

	cmdlineFields := []string{"tmux", "-L", socketName, "-C", "attach-session", "-t", session}

	// The premise: both servers are up, and they are different servers.
	ourPID, err := tmuxUnder(t, ourBase, "-L", socketName, "display-message", "-p", "#{pid}").Output()
	require.NoError(t, err, "our own server must be answering, or this test proves nothing")
	theirPID, err := tmuxUnder(t, foreign, "-L", socketName, "display-message", "-p", "#{pid}").Output()
	require.NoError(t, err)
	require.NotEqual(t, strings.TrimSpace(string(ourPID)), strings.TrimSpace(string(theirPID)),
		"the two bases must hold DIFFERENT servers, or there is no wrong server to reach")

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, cmdlineFields)

	assert.False(t, ok,
		"a candidate whose own socket is not the one the query reaches must be unclassifiable; "+
			"ok=true here is a kill verdict against a client that is attached and alive")
	assert.False(t, live)

	// And the verdict must be reached without signalling anything.
	time.Sleep(200 * time.Millisecond)
	assert.NoError(t, syscall.Kill(pid, 0), "the live foreign client was signalled")
}

// TestIsLiveTmuxClientOrServer_AcceptsACandidateOnOurOwnSocket is the positive
// control. Same-socket candidates are the whole population the sweep is for, so
// a socket comparison that refuses them too would close the hole by making the
// sweep inert — the failure mode this file's subject has twice already.
func TestIsLiveTmuxClientOrServer_AcceptsACandidateOnOurOwnSocket(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resolving a candidate's own socket reads /proc/<pid>/environ; linux-only by construction")
	}
	const socketName = "adsamesrv"
	const session = "agentdeck_same_server"

	ourBase := os.Getenv("TMUX_TMPDIR")
	require.NotEmpty(t, ourBase)
	startServerUnder(t, ourBase, socketName, session)
	pid := attachedClientUnder(t, ourBase, socketName, session)

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid,
		[]string{"tmux", "-L", socketName, "-C", "attach-session", "-t", session})

	assert.True(t, ok, "a candidate on the socket we resolve to must still be classifiable")
	assert.True(t, live, "an attached client on our own server must read as live")
}

// TestCandidateSocketPath_RefusesAnUnreadableEnvironment covers the other half
// of "resolve the candidate's socket from the candidate": a pid whose
// environment cannot be read cannot have its socket established either, and
// unestablished is a refusal, not a default.
func TestCandidateSocketPath_RefusesAnUnreadableEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/<pid>/environ")
	}
	// A pid that does not exist: /proc/<pid>/environ is as unreadable as it
	// gets, without needing a second uid.
	_, ok := candidateSocketPath(1<<30, []string{"tmux", "-L", "whatever", "list-clients"})

	assert.False(t, ok, "an unreadable environment must not resolve to a socket")
}
