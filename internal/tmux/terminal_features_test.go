package tmux

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tmuxDefaultFeatures is the array a stock tmux server reports before anyone
// touches it. Used as the "other people's entries" baseline.
var tmuxDefaultFeatures = []string{
	"xterm*:clipboard:ccolour:cstyle:focus:title",
	"screen*:title",
	"rxvt*:ignorefkeys",
}

func TestTerminalFeatureArgsFor(t *testing.T) {
	joined := func(args []string) string { return strings.Join(args, " ") }

	// No server to read (Session.Start emits its chunk in the same command that
	// creates the session): append once. Bounded, because every later pass CAN
	// read and short-circuits.
	unknown := terminalFeatureArgsFor(terminalFeatureState{known: false})
	assert.Equal(t, "; set -asq terminal-features ,*:hyperlinks:extkeys", joined(unknown),
		"unreadable server must still get the entry, via the quiet append")

	// Already present exactly once: emit nothing. This is the assertion that
	// makes repeat setup a no-op instead of a leak (#2061).
	present := terminalFeatureArgsFor(terminalFeatureState{
		known:  true,
		values: append(append([]string{}, tmuxDefaultFeatures...), agentDeckTerminalFeature),
	})
	assert.Empty(t, present, "entry already present must produce no tmux write, got: %s", joined(present))

	// Absent from a readable server: one whole-array set that keeps every
	// existing entry, in order, and adds ours at the end.
	absent := terminalFeatureArgsFor(terminalFeatureState{known: true, values: tmuxDefaultFeatures})
	assert.Equal(t,
		"; set -sq terminal-features xterm*:clipboard:ccolour:cstyle:focus:title,screen*:title,rxvt*:ignorefkeys,*:hyperlinks:extkeys",
		joined(absent))

	// An already-inflated server heals: the duplicates agent-deck created
	// collapse to one, and nothing else is touched. This is what an existing
	// user's server looks like on the first run after the fix.
	inflated := append([]string{}, tmuxDefaultFeatures...)
	for range 5000 {
		inflated = append(inflated, agentDeckTerminalFeature)
	}
	healed := terminalFeatureArgsFor(terminalFeatureState{known: true, values: inflated})
	assert.Equal(t,
		"; set -sq terminal-features xterm*:clipboard:ccolour:cstyle:focus:title,screen*:title,rxvt*:ignorefkeys,*:hyperlinks:extkeys",
		joined(healed), "5000 duplicates must collapse to one entry")
	// The healing write must stay small: an argv carrying the 5,003-entry value
	// back would be ~220 KB and can exceed ARG_MAX on macOS, which is exactly
	// the platform the leak was reported on.
	assert.Less(t, len(joined(healed)), 4096, "healing write must not carry the inflated value back")

	// Duplicates of OTHER entries are none of our business — agent-deck did not
	// create them and the option is shared with the user's own tmux config.
	userDupes := []string{"xterm*:title", "xterm*:title", agentDeckTerminalFeature}
	assert.Empty(t, terminalFeatureArgsFor(terminalFeatureState{known: true, values: userDupes}),
		"foreign duplicates must be left alone")

	// A value we cannot round-trip through one comma-joined set means our read
	// is not the whole truth. Never rewrite the array then: append if our entry
	// is missing, do nothing if it is there.
	unsafe := []string{"xterm*:title,screen*:title", "rxvt*:ignorefkeys"}
	assert.Equal(t, "; set -asq terminal-features ,*:hyperlinks:extkeys",
		joined(terminalFeatureArgsFor(terminalFeatureState{known: true, values: unsafe})))
	assert.Empty(t, terminalFeatureArgsFor(terminalFeatureState{
		known:  true,
		values: append(unsafe, agentDeckTerminalFeature),
	}), "unsafe array with our entry present must produce no write")
}

func TestClassifyTerminalFeatures(t *testing.T) {
	// A clean exit with no output is an EMPTY array, not an unreadable one —
	// `set -s terminal-features ""` produces exactly this, and the right answer
	// is to set our entry rather than blind-append to it.
	empty := classifyTerminalFeatures(nil, nil)
	assert.True(t, empty.known, "clean exit with no output is an empty array, not unknown")
	assert.Empty(t, empty.values)
	assert.Equal(t, "; set -sq terminal-features *:hyperlinks:extkeys",
		strings.Join(terminalFeatureArgsFor(empty), " "))

	// No server / unknown option / a client killed at its deadline: unknown.
	// Rewriting the array from nothing here would wipe it.
	assert.False(t, classifyTerminalFeatures(nil, errors.New("no server running")).known)
	assert.False(t, classifyTerminalFeatures([]byte("xterm*:title\n"), errors.New("boom")).known)

	// WaitDelay: bytes that arrived before cmd.Wait abandoned the stdio
	// goroutine are authoritative, but an empty body under it is indeterminate
	// — the values may simply never have been copied.
	partial := classifyTerminalFeatures([]byte("xterm*:title\nscreen*:title\n"), exec.ErrWaitDelay)
	assert.True(t, partial.known)
	assert.Equal(t, []string{"xterm*:title", "screen*:title"}, partial.values)
	assert.False(t, classifyTerminalFeatures(nil, exec.ErrWaitDelay).known,
		"empty body under WaitDelay must not be mistaken for an empty array")
}

func TestPlanTerminalFeatures_PreservesOrder(t *testing.T) {
	desired, changed := planTerminalFeatures([]string{
		"a:x", agentDeckTerminalFeature, "b:y", agentDeckTerminalFeature, "c:z",
	})
	require.True(t, changed)
	assert.Equal(t, []string{"a:x", agentDeckTerminalFeature, "b:y", "c:z"}, desired,
		"the first occurrence keeps its position; later copies drop out")

	same, changed := planTerminalFeatures([]string{"a:x", agentDeckTerminalFeature})
	assert.False(t, changed)
	assert.Equal(t, []string{"a:x", agentDeckTerminalFeature}, same)
}

// startPrivateTmuxServer boots a tmux server on a socket NAME that exists only
// for this test, under the isolated TMUX_TMPDIR this package's TestMain
// installs, and tears it down through the SAME socket resolution used to spawn
// it (tmuxExec, i.e. `tmux -L <name>`). The user's default socket is never
// addressed — see default_socket_guard.go, which refuses that outright.
//
// `-f /dev/null` keeps ~/.tmux.conf out of it: agent-deck's installer block
// appends a terminal-features entry of its own, which would make the counts here
// depend on the developer's machine.
func startPrivateTmuxServer(t *testing.T) (socket, session string) {
	t.Helper()
	skipIfNoTmuxBinary(t)
	socket = "ad2061-" + generateShortID()
	session = "tf-" + generateShortID()
	t.Cleanup(func() { _ = tmuxExec(socket, "kill-server").Run() })
	startPrivateTmuxSession(t, socket, session)
	return socket, session
}

// startPrivateTmuxSession starts (or restarts) a server on an existing private
// socket name and waits for it to answer, so a caller can replace the server
// behind a socket the way real life does.
func startPrivateTmuxSession(t *testing.T, socket, session string) {
	t.Helper()
	out, err := tmuxExec(socket, "-f", "/dev/null", "new-session", "-d", "-s", session,
		"sh", "-c", "sleep 300").CombinedOutput()
	require.NoError(t, err, "start private tmux server: %s", strings.TrimSpace(string(out)))
	require.Eventually(t, func() bool {
		return readTerminalFeatures(socket).known
	}, 5*time.Second, 20*time.Millisecond, "private tmux server did not come up")
}

// tmuxServerPID identifies the tmux SERVER behind a socket name. Two different
// pids under the same socket name are two different servers, which is the whole
// point of TestTerminalFeatures_ReplacedServerStillGetsTheEntry.
func tmuxServerPID(t *testing.T, socket string) string {
	t.Helper()
	out, err := tmuxExec(socket, "display-message", "-p", "#{pid}").Output()
	require.NoError(t, err, "read tmux server pid")
	return strings.TrimSpace(string(out))
}

// TestTerminalFeatures_ControlClientRespawnDoesNotGrow is the #2061 red path.
//
// The reporter's server reached 5,018 entries (219.8 KB) and a live server was
// measured at 36,355 — growth of ~3-5 entries per MINUTE with 13 sessions and no
// new sessions being created. What repeats is not session creation: it is
// agent-deck's per-session configuration pass (EnsureConfigured -> EnableMouseMode)
// re-running for a session that is already alive — after a storage reload, in a
// newly started process, alongside the control clients the TUI respawns
// continuously. Each pass ran `set -as terminal-features ,*:hyperlinks:extkeys`
// against the long-lived SERVER, so the array grew once per pass forever.
//
// So the loop below respawns a real control-mode client and re-runs the
// configuration pass. Nothing is cached between passes, deliberately: the state
// that grew lives in the server, so idempotence has to hold at the tmux level
// and not in a process-lifetime memo that dies with the process (see
// TestTerminalFeatures_ReplacedServerStillGetsTheEntry for the other direction).
//
// Without the fix this fails with one entry per iteration.
func TestTerminalFeatures_ControlClientRespawnDoesNotGrow(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)

	before := readTerminalFeatures(socket)
	require.True(t, before.known, "must be able to read terminal-features from the private server")
	require.Equal(t, 0, countTerminalFeature(before.values), "fresh server must not carry our entry")

	sess := &Session{Name: session, SocketName: socket, mouse: true}

	const respawns = 20
	for range respawns {
		ks, err := OpenKeySender(socket, session)
		require.NoError(t, err, "spawn control-mode client")
		require.NoError(t, ks.Close())

		require.NoError(t, sess.EnableMouseMode())
	}

	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Equal(t, 1, countTerminalFeature(after.values),
		"%d control-client respawns + configuration passes must leave exactly one entry, got array: %v",
		respawns, after.values)
	assert.Len(t, after.values, len(before.values)+1,
		"the server-wide array must grow by our single entry and never again")
	for _, existing := range before.values {
		assert.Contains(t, after.values, existing, "pre-existing entry must survive")
	}
}

// TestTerminalFeatures_CollapsesInflatedServer covers the state every existing
// user is already in: a server carrying thousands of duplicates that a version
// upgrade alone cannot shrink, because the damage lives in the tmux server and
// nobody restarts it. The first configuration pass after the fix must collapse
// it instead of merely stopping the growth.
func TestTerminalFeatures_CollapsesInflatedServer(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)

	baseline := readTerminalFeatures(socket)
	require.True(t, baseline.known)

	// Inflate the way the old code did: one append per configuration pass.
	const inflation = 200
	for range inflation {
		require.NoError(t, tmuxExec(socket, "set", "-asq", "terminal-features",
			","+agentDeckTerminalFeature).Run())
	}
	inflated := readTerminalFeatures(socket)
	require.Equal(t, inflation, countTerminalFeature(inflated.values),
		"pre-condition: the historical append must have grown the array")

	sess := &Session{Name: session, SocketName: socket, mouse: true}
	require.NoError(t, sess.EnableMouseMode())

	healed := readTerminalFeatures(socket)
	require.True(t, healed.known)
	assert.Equal(t, 1, countTerminalFeature(healed.values), "inflated array must collapse to one entry")
	assert.Len(t, healed.values, len(baseline.values)+1)
	for _, existing := range baseline.values {
		assert.Contains(t, healed.values, existing, "tmux default must survive the collapse")
	}
}

// TestTerminalFeatures_EmptyArrayServerGetsOneEntry covers the server whose
// array a user emptied outright (`set -s terminal-features ""`). tmux reports
// that as a clean read with no output, and the pass must set our entry once and
// then leave it alone — not append to it every time.
func TestTerminalFeatures_EmptyArrayServerGetsOneEntry(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	require.NoError(t, tmuxExec(socket, "set", "-s", "terminal-features", "").Run())
	require.Empty(t, readTerminalFeatures(socket).values, "pre-condition: the array is empty")

	sess := &Session{Name: session, SocketName: socket, mouse: true}
	for range 5 {
		require.NoError(t, sess.EnableMouseMode())
	}

	got := readTerminalFeatures(socket)
	require.True(t, got.known)
	assert.Equal(t, []string{agentDeckTerminalFeature}, got.values,
		"an emptied array must end up holding exactly our entry")
}

// TestTerminalFeatures_OverrideIsAuthoritative pins the #1625 escape hatch that
// #2061 reported as the only workaround: an explicit [tmux.options]
// terminal-features value means agent-deck writes nothing of its own.
func TestTerminalFeatures_OverrideIsAuthoritative(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)

	sess := &Session{
		Name:            session,
		SocketName:      socket,
		mouse:           true,
		OptionOverrides: map[string]string{"terminal-features": "xterm*:hyperlinks"},
	}
	for range 5 {
		require.NoError(t, sess.EnableMouseMode())
	}

	got := readTerminalFeatures(socket)
	require.True(t, got.known)
	assert.Equal(t, 0, countTerminalFeature(got.values),
		"an explicit override must suppress agent-deck's entry entirely, got: %v", got.values)
}

// TestTerminalFeatures_ReplacedServerStillGetsTheEntry is the second red path,
// and it points the opposite way to the first.
//
// Round 1 of this fix remembered, per socket, that the process had already seen
// the entry, and skipped both the read and the write from then on. A tmux socket
// NAME is not a tmux server IDENTITY: the server exits when its last session
// closes (or crashes), and the next session starts a brand-new server under the
// same name. The memo then reported "settled" for a server that had never been
// written to, so that server never got hyperlinks or extended keys for the rest
// of the process's life — an unbounded-write bug traded for a never-write bug,
// silent in both directions.
//
// So: settle the entry, replace the server behind the socket, and require that
// the next configuration pass write it again. This fails against any
// process-lifetime memo keyed on the socket name.
func TestTerminalFeatures_ReplacedServerStillGetsTheEntry(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	sess := &Session{Name: session, SocketName: socket, mouse: true}

	// Two passes against the FIRST server: one to write the entry, one that
	// finds it already there. The second is what armed the round-1 memo.
	require.NoError(t, sess.EnableMouseMode())
	require.NoError(t, sess.EnableMouseMode())
	require.Equal(t, 1, countTerminalFeature(readTerminalFeatures(socket).values),
		"pre-condition: the entry is settled on the first server")
	firstPID := tmuxServerPID(t, socket)

	// The server goes away and a new one takes over the same socket name.
	require.NoError(t, tmuxExec(socket, "kill-server").Run())
	require.Eventually(t, func() bool {
		return !readTerminalFeatures(socket).known
	}, 5*time.Second, 20*time.Millisecond, "old server did not go away")
	startPrivateTmuxSession(t, socket, session)
	require.NotEqual(t, firstPID, tmuxServerPID(t, socket), "must be a different tmux server")

	fresh := readTerminalFeatures(socket)
	require.True(t, fresh.known)
	require.Equal(t, 0, countTerminalFeature(fresh.values),
		"pre-condition: the replacement server starts without our entry")

	require.NoError(t, sess.EnableMouseMode())

	after := readTerminalFeatures(socket)
	require.True(t, after.known)
	assert.Equal(t, 1, countTerminalFeature(after.values),
		"a server replaced behind the same socket name must still be configured, got: %v", after.values)
	assert.Len(t, after.values, len(fresh.values)+1)
}

// TestTerminalFeatures_ExactlyOncePerServerLifetime states the contract both red
// paths defend, in one place: however many passes run, the entry ends up present
// exactly once per server — and "per server" means per server, not per socket
// name.
func TestTerminalFeatures_ExactlyOncePerServerLifetime(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	sess := &Session{Name: session, SocketName: socket, mouse: true}

	for generation := range 3 {
		if generation > 0 {
			require.NoError(t, tmuxExec(socket, "kill-server").Run())
			require.Eventually(t, func() bool {
				return !readTerminalFeatures(socket).known
			}, 5*time.Second, 20*time.Millisecond, "old server did not go away")
			startPrivateTmuxSession(t, socket, session)
		}
		baseline := readTerminalFeatures(socket)
		require.True(t, baseline.known)
		for range 4 {
			require.NoError(t, sess.EnableMouseMode())
		}
		got := readTerminalFeatures(socket)
		assert.Equal(t, 1, countTerminalFeature(got.values),
			"server generation %d: expected exactly one entry, got: %v", generation, got.values)
		assert.Len(t, got.values, len(baseline.values)+1,
			"server generation %d: array grew by more than our entry", generation)
	}
}
