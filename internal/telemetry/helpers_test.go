package telemetry

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testKey = "phc_testkey0123456789abcdefXYZ"

// clock is a settable nowFn.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }

func (c *clock) set(t time.Time) { c.mu.Lock(); c.t = t; c.mu.Unlock() }

func (c *clock) add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// day0 is the consent day used throughout; hours are local wall clock.
func at(dayOffset, hour, minute int) time.Time {
	return time.Date(2026, 9, 26+dayOffset, hour, minute, 0, 0, time.Local)
}

// env isolates HOME, clears every marker, lifts the test-binary hard-off
// for this test, and pins TTY, surface, version and the clock.
func env(t *testing.T) *clock {
	t.Helper()
	EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	keys := append([]string{EnvTelemetry, EnvDoNotTrack, EnvPostHogKey}, sessionMarkers...)
	keys = append(append(keys, ciMarkers...), agentMarkers...)
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	prevTTY, prevDisabled, prevUnreadable := isTerminalFn, configDisabled, configUnreadable
	prevKey, prevLevel, prevEndpoint := configKey, configLevel, endpoint
	prevNow, prevSurface, prevVersion, prevUUID := nowFn, surface, processVersion, newUUID
	t.Cleanup(func() {
		isTerminalFn, configDisabled, configUnreadable = prevTTY, prevDisabled, prevUnreadable
		configKey, configLevel, endpoint = prevKey, prevLevel, prevEndpoint
		nowFn, surface, processVersion, newUUID = prevNow, prevSurface, prevVersion, prevUUID
	})
	isTerminalFn = func() bool { return true }
	configDisabled, configUnreadable = false, false
	configKey, configLevel, endpoint = "", "", DefaultEndpoint
	SetProcess("9.9.9", SurfaceTUI)
	c := &clock{t: at(0, 14, 30)}
	nowFn = c.now
	return c
}

// grant consents on the clock's current day and returns the saved state.
func grant(t *testing.T, c *clock) *State {
	t.Helper()
	s := LoadState()
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	return LoadState()
}

// fakePostHog records every request to /batch/.
type fakePostHog struct {
	srv        *httptest.Server
	mu         sync.Mutex
	bodies     [][]byte
	headers    []http.Header
	paths      []string
	status     atomic.Int32
	retryAfter atomic.Value
	block      chan struct{}
}

func newFakePostHog(t *testing.T) *fakePostHog {
	t.Helper()
	f := &fakePostHog{}
	f.status.Store(http.StatusOK)
	f.retryAfter.Store("")
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, body)
		f.headers = append(f.headers, r.Header.Clone())
		f.paths = append(f.paths, r.URL.Path)
		block := f.block
		f.mu.Unlock()
		if block != nil {
			<-block
		}
		if ra := f.retryAfter.Load().(string); ra != "" {
			w.Header().Set("Retry-After", ra)
		}
		w.WriteHeader(int(f.status.Load()))
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(f.srv.Close)
	endpoint = f.srv.URL
	t.Setenv(EnvPostHogKey, testKey)
	return f
}

func (f *fakePostHog) hits() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.bodies) }

func (f *fakePostHog) batch(t *testing.T, i int) phBatch {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	var b phBatch
	if err := json.Unmarshal(f.bodies[i], &b); err != nil {
		t.Fatalf("request %d is not a batch: %v", i, err)
	}
	return b
}

// spoolLines reads the spool as the uploader would.
func spoolLines(t *testing.T) []spoolLine {
	t.Helper()
	lines, err := readSpool()
	if err != nil {
		t.Fatal(err)
	}
	return lines
}

func spoolBytes(t *testing.T) []byte {
	t.Helper()
	p, err := spoolPath()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return b
}

func eventNames(lines []spoolLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l.E
		if step, ok := l.P["step"].(string); ok {
			out[i] += ":" + step
		}
	}
	return out
}

func countEvent(lines []spoolLine, name string) int {
	n := 0
	for _, l := range lines {
		if l.E == name {
			n++
		}
	}
	return n
}

// sequentialUUIDs makes spool uuids deterministic.
func sequentialUUIDs(t *testing.T) {
	t.Helper()
	var n atomic.Int64
	newUUID = func() string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n.Add(1)) }
}

func writeV1State(t *testing.T, consent Consent) {
	t.Helper()
	p, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"revision":3,"schema_version":1,"consent":%q,"consent_endpoint":"https://telemetry.agent-deck.invalid/v1/ping","install_id":%q,"counters":{"tui_launches":4}}`,
		consent, strings.Repeat("a", 32))
	if err := os.WriteFile(p, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}
