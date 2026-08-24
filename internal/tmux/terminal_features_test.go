package tmux

import (
	"strings"
	"testing"

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
	t.Cleanup(func() {
		_ = tmuxExec(socket, "kill-server").Run()
		forgetTerminalFeatureSettled(socket)
	})
	out, err := tmuxExec(socket, "-f", "/dev/null", "new-session", "-d", "-s", session,
		"sh", "-c", "sleep 300").CombinedOutput()
	require.NoError(t, err, "start private tmux server: %s", strings.TrimSpace(string(out)))
	return socket, session
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
// configuration pass, with the per-socket memo cleared each round to model a
// FRESH agent-deck process every time — the memo must not be the only thing
// holding the line, because it dies with the process and the server does not.
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

		forgetTerminalFeatureSettled(socket)
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
		forgetTerminalFeatureSettled(socket)
		require.NoError(t, sess.EnableMouseMode())
	}

	got := readTerminalFeatures(socket)
	require.True(t, got.known)
	assert.Equal(t, 0, countTerminalFeature(got.values),
		"an explicit override must suppress agent-deck's entry entirely, got: %v", got.values)
}

// TestTerminalFeatures_SettledMemoStopsProbing pins the steady state: once a
// process has SEEN the entry on a socket, later passes cost no tmux subprocess
// at all. Start and EnableMouseMode run on hot paths, so a per-pass read would
// trade one bug for a slower one.
func TestTerminalFeatures_SettledMemoStopsProbing(t *testing.T) {
	socket, session := startPrivateTmuxServer(t)
	sess := &Session{Name: session, SocketName: socket, mouse: true}

	require.NoError(t, sess.EnableMouseMode(), "first pass must write the entry")
	assert.False(t, terminalFeatureIsSettled(socket), "the writing pass must not assume its write landed")
	assert.Empty(t, sess.terminalFeatureArgs(), "second pass sees the entry and writes nothing")
	assert.True(t, terminalFeatureIsSettled(socket), "a verified read settles the socket")

	// Settled means no more reads: kill the server and ask again. A pass that
	// still probed would come back "unreadable" and emit the append.
	require.NoError(t, tmuxExec(socket, "kill-server").Run())
	assert.Empty(t, sess.terminalFeatureArgs(), "a settled socket must not be probed again")
}
