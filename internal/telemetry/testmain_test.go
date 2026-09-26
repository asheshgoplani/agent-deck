package telemetry

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync/atomic"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// nonLoopbackRequests counts requests the guard refused. Any is a failure:
// tests only ever talk to a fake PostHog on loopback.
var nonLoopbackRequests atomic.Int32

// loopbackOnly fails every request that is not to a loopback host, so no
// test can reach PostHog (or anything else) even by mistake.
type loopbackOnly struct{ next http.RoundTripper }

func (l loopbackOnly) RoundTrip(r *http.Request) (*http.Response, error) {
	host := r.URL.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		nonLoopbackRequests.Add(1)
		return nil, errors.New("test guard: non-loopback telemetry request to " + host)
	}
	return l.next.RoundTrip(r)
}

func runTestMain(m *testing.M) int {
	// Never resolve paths under the developer's real HOME.
	cleanupTmux := testutil.IsolateTmuxSocket()
	defer cleanupTmux()
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()
	os.Setenv("AGENTDECK_PROFILE", "_test")
	httpClient.Transport = loopbackOnly{next: &http.Transport{Proxy: nil}}
	code := m.Run()
	if n := nonLoopbackRequests.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d telemetry request(s) to a non-loopback host\n", n)
		return 1
	}
	return code
}
