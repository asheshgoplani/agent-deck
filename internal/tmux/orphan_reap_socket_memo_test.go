package tmux

import (
	"context"
	"os/exec"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isLiveTmuxClientOrServer spawns two tmux clients per candidate and memoizes
// nothing. On the host this sweep exists for, that is up to two spawns per
// wedged orphan — each one a fresh client against the same unresponsive server,
// and so itself a candidate for the fd leak being cleaned up — with each pair
// riding tmuxLiveQueryTimeout, so a handful of candidates can spend the whole
// shared budget and the rest are reported unexamined.
//
// The obvious fix is to cache the answers, and the PR body rejects it for a
// real reason: a cached client list goes stale inside one sweep, and a client
// that connects mid-sweep would then be absent from the cache and eligible to
// be killed. Freshness is the safety property.
//
// What IS cacheable is the refusal. "This socket could not be reached" only
// ever produces more refusals, never a kill, so memoizing it is fail-closed by
// construction — and it is precisely the expensive case, since an unreachable
// or wedged server is what makes a query ride its full cap.

func TestIsLiveTmuxClientOrServer_MemoizesAnUnreachableSocketForTheSweep(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("resolving a candidate's own socket reads /proc/<pid>/environ; linux-only by construction")
	}
	socket := orphanLiveTestSocket(t)
	resetUnreachableSockets()

	// A live candidate whose argv points at a socket with no server on it.
	pid := fakeTmuxCandidateArgv(t, "ignore",
		[]string{"-L", socket, "list-clients", "-t", SessionPrefix + "memo_probe"})
	cmdlineFields := []string{"tmux", "-L", socket, "list-clients", "-t", SessionPrefix + "memo_probe"}

	live, ok := isLiveTmuxClientOrServer(context.Background(), pid, cmdlineFields)
	require.False(t, ok, "no server on that socket yet, so the candidate must be unclassifiable")
	require.False(t, live)

	// Now stand a real server up on that same socket, mid-sweep. Without the
	// memo the next call reaches it, finds neither its server pid nor any of
	// its clients matching, and answers live=false ok=true — a KILL verdict,
	// produced by a server that was not even running when this sweep started
	// judging the candidate.
	startOrphanLiveSession(t, socket, "agentdeck_memo_probe")
	out, err := exec.Command("tmux", "-L", socket, "display-message", "-p", "#{pid}").Output()
	require.NoError(t, err, "the server must be answering, or this proves nothing")
	require.NotEmpty(t, out)

	live, ok = isLiveTmuxClientOrServer(context.Background(), pid, cmdlineFields)

	assert.False(t, ok,
		"a socket already found unreachable in this sweep must stay refused: re-querying it "+
			"both spends the budget again and lets a server that appeared mid-sweep produce a "+
			"kill verdict for a candidate judged against its absence")
	assert.False(t, live)
}

// TestResetUnreachableSockets_ClearsBetweenSweeps pins the scope of the memo.
// It is a within-one-sweep optimisation; carrying it across sweeps would make a
// socket that was briefly unreachable permanently unreapable, which is the
// inert-sweep failure this area started with.
func TestResetUnreachableSockets_ClearsBetweenSweeps(t *testing.T) {
	resetUnreachableSockets()
	markSocketUnreachable("/tmp/some-socket")
	require.True(t, socketKnownUnreachable("/tmp/some-socket"))

	resetUnreachableSockets()

	assert.False(t, socketKnownUnreachable("/tmp/some-socket"),
		"the memo must not survive into the next sweep")
}

// TestReapOrphanedPollClients_ClearsTheMemoAtTheStartOfASweep pins the WIRING,
// not just the helper. A reset function nothing calls is the same defect as no
// reset at all: the memo would survive into the next sweep and a socket that
// was briefly unreachable would be permanently unreapable.
//
// Every candidate reads as live here, so this exercises the real sweep over the
// real /proc without a single signal being sent.
func TestReapOrphanedPollClients_ClearsTheMemoAtTheStartOfASweep(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the sweep is linux-only by construction")
	}
	stubLiveTmuxIdentity(t, func(context.Context, int, []string) (bool, bool) { return true, true })

	const stale = "/tmp/unreachable-in-the-previous-sweep"
	markSocketUnreachable(stale)
	require.True(t, socketKnownUnreachable(stale))

	reapOrphanedPollClients()

	assert.False(t, socketKnownUnreachable(stale),
		"the sweep must clear the memo as it starts; a reset nobody calls is not a reset")
}
