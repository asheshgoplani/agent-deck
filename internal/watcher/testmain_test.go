package watcher

import (
	"fmt"
	"os"
	"testing"

	"go.uber.org/goleak"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

// TestMain wraps the watcher package tests with goleak verification.
//
// The IgnoreTopFunction / IgnoreAnyFunction filters cover background
// goroutines that Google client libraries spawn during init and that do
// not shut down on client.Close — these are framework-level background
// workers we cannot drain, so they must be filtered to avoid false
// positives from goleak.
//
//   - go.opencensus.io/stats/view.(*worker).start
//     Started by go.opencensus.io stats view init; outlives client.Close.
//     EMPIRICALLY VERIFIED in Plan 17-01 via TestSpike_PubsubGoleakFilters.
//
//   - google.golang.org/grpc.(*ccBalancerWrapper).watcher
//     grpc client-conn balancer watcher loop (transport reconnect path).
//
//   - google.golang.org/grpc.(*ccResolverWrapper).watcher
//     grpc resolver watcher loop.
//
//   - google.golang.org/grpc.(*addrConn).resetTransport
//     grpc connection reset / retry goroutine.
//
//   - google.golang.org/grpc/internal/transport.(*http2Client).keepalive
//     HTTP/2 keepalive pinger for open transports.
//
//   - google.golang.org/grpc/internal/transport.newHTTP2Client (IgnoreAnyFunction)
//     Covers any goroutine spawned from within newHTTP2Client (writer,
//     reader, goAway handlers) which all live until transport shutdown.
//
//   - database/sql.(*DB).connectionOpener
//
//   - database/sql.(*DB).connectionResetter
//     Connection-pool workers for statedb-backed watcher tests.
//
//   - modernc.org (IgnoreAnyFunction)
//     modernc/sqlite finalizer goroutines.
//
//   - internal/poll.runtime_pollWait (IgnoreAnyFunction)
//     Parked netpoll goroutines from background HTTP clients.
//
// Filter set empirically verified in Plan 17-01 via TestSpike_PubsubGoleakFilters.
// The spike observed only go.opencensus.io/stats/view.(*worker).start after
// pstest + pubsub.Client teardown on this environment; the broader grpc filters
// are kept preemptively because they are the canonical leak surface documented
// in RESEARCH.md §Pitfall 1 and will fire as soon as a real pubsub.Subscription
// is Receive()-d in Plan 17-02.
func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the isolation cleanups actually
// run. goleak.VerifyTestMain calls os.Exit internally, and os.Exit does NOT run
// deferred functions — the previous code acknowledged that and discarded both
// cleanup funcs on the theory that "the temp dir is reaped by the OS". macOS
// does not reap $TMPDIR between runs, so this package stranded an ad-home-* and
// an ad-tmux-* dir on every single run (2026-08-10 temp-leak incident).
//
// Inlining VerifyTestMain's body — m.Run, then goleak.Find on success — gets the
// identical leak verification while leaving a return path for the defers. Find
// runs BEFORE the cleanups so tmux teardown subprocesses cannot register as
// goroutine leaks.
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5). Must run
	// before any path is resolved. See internal/testutil/homeenv.go.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	// Isolate the tmux socket so tests never spawn tmux on the user's default
	// socket. See internal/testutil/tmuxenv.go for the 2026-04-17 postmortem.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()

	os.Setenv("AGENTDECK_PROFILE", "_test")

	code := m.Run()
	if code == 0 {
		if err := goleak.Find(
			goleak.IgnoreTopFunction("go.opencensus.io/stats/view.(*worker).start"),
			goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccBalancerWrapper).watcher"),
			goleak.IgnoreTopFunction("google.golang.org/grpc.(*ccResolverWrapper).watcher"),
			goleak.IgnoreTopFunction("google.golang.org/grpc.(*addrConn).resetTransport"),
			goleak.IgnoreTopFunction("google.golang.org/grpc/internal/transport.(*http2Client).keepalive"),
			goleak.IgnoreAnyFunction("google.golang.org/grpc/internal/transport.newHTTP2Client"),
			goleak.IgnoreTopFunction("database/sql.(*DB).connectionOpener"),
			goleak.IgnoreTopFunction("database/sql.(*DB).connectionResetter"),
			goleak.IgnoreAnyFunction("modernc.org"),
			goleak.IgnoreAnyFunction("internal/poll.runtime_pollWait"),
		); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: Errors on successful test run: %v\n", err)
			code = 1
		}
	}
	return code
}
