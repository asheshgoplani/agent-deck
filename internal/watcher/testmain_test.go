package watcher

import (
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
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5). goleak's
	// VerifyTestMain calls os.Exit internally so the cleanup func cannot run,
	// which is fine — the temp dir is reaped by the OS. What matters is HOME is
	// overridden BEFORE any path is resolved. See internal/testutil/homeenv.go.
	_ = testutil.IsolateHome()

	// Isolate the tmux socket. goleak.VerifyTestMain calls os.Exit internally,
	// so the cleanup func cannot run here — that's acceptable because the temp
	// directory is harmless and will be reaped by the OS. What matters is that
	// TMUX_TMPDIR is set BEFORE any test in this package spawns a tmux process.
	// See internal/testutil/tmuxenv.go for the 2026-04-17 postmortem.
	_ = testutil.IsolateTmuxSocket()

	os.Setenv("AGENTDECK_PROFILE", "_test")
	goleak.VerifyTestMain(m,
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
	)
}
