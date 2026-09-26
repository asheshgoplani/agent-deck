package telemetry

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUploadHappyPathMapsToPostHogBatch(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	s := grant(t, c)
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaTUINew, SessionID: "abc"})
	FeatureUsed("fork", false)
	if r := MaybeUpload(t.Context()); r.Attempted || !strings.Contains(r.Reason, "consent day") {
		t.Fatalf("consent day upload: %+v", r)
	}
	c.set(at(1, 9, 0))
	r := MaybeUpload(t.Context())
	if !r.Sent || fake.hits() != 1 {
		t.Fatalf("upload: %+v hits %d", r, fake.hits())
	}
	fake.mu.Lock()
	path, h := fake.paths[0], fake.headers[0]
	fake.mu.Unlock()
	if path != "/batch/" || h.Get("Content-Type") != "application/json" || h.Get("User-Agent") != "agent-deck/9.9.9" || h.Get("Cookie") != "" {
		t.Fatalf("request path %s headers %v", path, h)
	}
	b := fake.batch(t, 0)
	if b.APIKey != testKey {
		t.Fatalf("api_key %q", b.APIKey)
	}
	var sawCreate, sawFeature bool
	for _, ev := range b.Batch {
		if ev.DistinctID != s.InstallID || !uuidPattern.MatchString(ev.UUID) {
			t.Fatalf("%s: distinct_id %s uuid %s", ev.Event, ev.DistinctID, ev.UUID)
		}
		switch ev.Event {
		case "session.create":
			sawCreate = true
			p := ev.Properties
			if ev.Timestamp != "2026-09-26T14:00:00Z" || p["hour_local"] != float64(14) || p["weekday_local"] != float64(6) {
				t.Fatalf("floating time: %s %v %v", ev.Timestamp, p["hour_local"], p["weekday_local"])
			}
			if p["tool"] != "codex" || p["via"] != "tui_new" || p["install_age"] != "d0" || p["schema"] != float64(2) || p["$lib"] != "agent-deck" {
				t.Fatalf("props %v", p)
			}
			if ds, _ := p["ds_session"].(string); len(ds) != 16 || ds == "abc" {
				t.Fatalf("ds_session %v", p["ds_session"])
			}
		case "feature.daily":
			sawFeature = true
		}
	}
	if !sawCreate || !sawFeature {
		t.Fatal("batch lacks the spooled event or the daily rollup")
	}
	after := LoadState()
	if len(spoolLines(t)) != 0 || after.Daily[dayOf(at(0, 0, 0))] != nil || len(after.LastPayload) == 0 {
		t.Fatal("acknowledged data must leave the spool and rollups; show-last must keep the body")
	}
	if !after.Upload.NextTry.Equal(at(1, 9, 0).Add(uploadInterval)) || after.Upload.LastResult != resultOK {
		t.Fatalf("next try %v result %s", after.Upload.NextTry, after.Upload.LastResult)
	}
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaTUINew})
	c.add(2 * time.Hour)
	if r := MaybeUpload(t.Context()); r.Attempted {
		t.Fatal("uploaded again inside 6 hours")
	}
	c.add(5 * time.Hour)
	if r := MaybeUpload(t.Context()); !r.Sent || fake.hits() != 2 {
		t.Fatalf("6-hourly upload: %+v", r)
	}
}

func TestUploadSendsOnlyCompletedHours(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	c.set(at(1, 9, 10))
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 10, 20))
	SessionEnded(SessionEndInfo{Tool: "codex", Kind: EndStop})
	MaybeUpload(t.Context())
	b := fake.batch(t, 0)
	for _, ev := range b.Batch {
		if ev.Event == "session.end" && ev.Properties["tool"] == "codex" {
			t.Fatal("the current hour was uploaded")
		}
	}
	left := spoolLines(t)
	if len(left) != 1 || left[0].P["tool"] != "codex" {
		t.Fatalf("spool after upload %v", eventNames(left))
	}
	if LoadState().Daily[dayOf(c.now())] == nil {
		t.Fatal("today's rollup must wait for the day to complete")
	}
}

func TestUploadRetryBackoffAndDailyAttemptBudget(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	fake.status.Store(http.StatusServiceUnavailable)
	start := at(1, 1, 0)
	c.set(start)
	waits := []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}
	for i, w := range waits {
		r := MaybeUpload(t.Context())
		if !r.Attempted || r.Sent {
			t.Fatalf("attempt %d: %+v", i, r)
		}
		next := LoadState().Upload.NextTry
		if !next.Equal(c.now().Add(w)) {
			t.Fatalf("attempt %d: next %v want +%v", i, next, w)
		}
		c.set(next)
	}
	MaybeUpload(t.Context())
	if next := LoadState().Upload.NextTry; !next.Equal(at(2, 0, 0)) {
		t.Fatalf("fourth failure: next %v, want next local day", next)
	}
	if fake.hits() != 4 || len(spoolLines(t)) != 1 {
		t.Fatalf("hits %d spool %d", fake.hits(), len(spoolLines(t)))
	}
	s := LoadState()
	s.Upload.NextTry = time.Time{}
	_ = SaveState(s)
	if r := MaybeUpload(t.Context()); r.Attempted {
		t.Fatal("a fifth attempt on the same day")
	}
	c.set(at(2, 0, 5))
	fake.status.Store(http.StatusOK)
	if r := MaybeUpload(t.Context()); !r.Sent {
		t.Fatalf("next day: %+v", r)
	}
}

func TestUploadHonoursRetryAfterUpToSixHours(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 1, 0))
	fake.status.Store(http.StatusTooManyRequests)
	fake.retryAfter.Store("120")
	MaybeUpload(t.Context())
	if next := LoadState().Upload.NextTry; !next.Equal(c.now().Add(2 * time.Minute)) {
		t.Fatalf("Retry-After 120: next %v", next)
	}
	c.add(2 * time.Minute)
	fake.retryAfter.Store("999999")
	MaybeUpload(t.Context())
	if next := LoadState().Upload.NextTry; !next.Equal(c.now().Add(maxRetryAfter)) {
		t.Fatalf("Retry-After capped: next %v", next)
	}
}

func TestUploadRejectionStopsUntilNewVersionThenDrops(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	fake.status.Store(http.StatusBadRequest)
	MaybeUpload(t.Context())
	if u := LoadState().Upload; u.RejectedVersion != "9.9.9" || u.LastErrorKind != errKindRejected {
		t.Fatalf("upload state %+v", u)
	}
	c.set(at(2, 9, 0))
	if r := MaybeUpload(t.Context()); r.Attempted || fake.hits() != 1 {
		t.Fatalf("retried a rejection on the same version: %+v", r)
	}
	if len(spoolLines(t)) != 1 {
		t.Fatal("data dropped before 3 days")
	}
	c.set(at(4, 9, 0))
	MaybeUpload(t.Context())
	if len(spoolLines(t)) != 0 || fake.hits() != 1 {
		t.Fatal("rejected data older than 3 days must be dropped without sending")
	}
	// A new agent-deck version tries again.
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(5, 9, 0))
	fake.status.Store(http.StatusOK)
	SetProcess("9.9.10", SurfaceTUI)
	if r := MaybeUpload(t.Context()); !r.Sent {
		t.Fatalf("new version: %+v", r)
	}
}

func TestUploadChunksAt500EventsAndFiveRequests(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	var lines []spoolLine
	for i := 0; i < 2700; i++ {
		h, w := 10, 6
		lines = append(lines, spoolLine{E: "session.end", U: newUUID(), D: "2026-09-26", H: &h, W: &w, S: i + 1, V: "9.9.9",
			A: "human", SF: "tui", L: "full", P: map[string]any{"tool": "claude", "end_kind": "stop"}})
	}
	if err := writeSpool(lines); err != nil {
		t.Fatal(err)
	}
	c.set(at(1, 9, 0))
	r := MaybeUpload(t.Context())
	if !r.Sent || fake.hits() != maxBatchRequests {
		t.Fatalf("%+v hits %d", r, fake.hits())
	}
	fake.mu.Lock()
	for i, body := range fake.bodies {
		if len(body) > maxBatchBytes {
			t.Errorf("request %d is %d bytes", i, len(body))
		}
	}
	fake.mu.Unlock()
	sent := 0
	for i := 0; i < maxBatchRequests; i++ {
		n := len(fake.batch(t, i).Batch)
		if n > maxBatchEvents || n == 0 {
			t.Fatalf("request %d carries %d events", i, n)
		}
		sent += n
	}
	if left := len(spoolLines(t)); left == 0 || left != 2700-sent {
		t.Fatalf("%d lines left after sending %d; want the unsent remainder", left, sent)
	}
}

func TestUploadRefusesRedirects(t *testing.T) {
	c := env(t)
	var target atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { target.Add(1) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/batch/", http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	endpoint = redirect.URL
	t.Setenv(EnvPostHogKey, testKey)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	if r := MaybeUpload(t.Context()); r.Sent || target.Load() != 0 || len(spoolLines(t)) != 1 {
		t.Fatalf("redirect followed: %+v", r)
	}
}

func TestUploadIgnoresProxyAndForeignEnvironment(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	for k, v := range map[string]string{
		"HTTPS_PROXY": "http://127.0.0.1:1", "HTTP_PROXY": "http://127.0.0.1:1", "ALL_PROXY": "http://127.0.0.1:1",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1", "POSTHOG_API_KEY": "phc_foreign0123456789abcdef",
		"POSTHOG_HOST": "http://127.0.0.1:1",
	} {
		t.Setenv(k, v)
	}
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	if r := MaybeUpload(t.Context()); !r.Sent || fake.batch(t, 0).APIKey != testKey {
		t.Fatalf("%+v", r)
	}
}

func TestNoKeyMeansSpoolOnlyAndNotConfigured(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	t.Setenv(EnvPostHogKey, "")
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	r := MaybeUpload(t.Context())
	if r.Attempted || !strings.Contains(r.Reason, "not configured") || fake.hits() != 0 || len(spoolLines(t)) != 1 {
		t.Fatalf("%+v hits %d", r, fake.hits())
	}
}

func TestDevBuildsCLIAndCINeverUpload(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T)
	}{
		{"dev", func(*testing.T) { SetProcess("dev", SurfaceTUI) }},
		{"build_string", func(*testing.T) { SetProcess("1.16.18-3-gabc123-dirty", SurfaceTUI) }},
		{"cli", func(*testing.T) { SetProcess("9.9.9", SurfaceCLI) }},
		{"ci", func(t *testing.T) { t.Setenv("CI", "true") }},
		{"invalid_host", func(*testing.T) { endpoint = "https://telemetry.agent-deck.invalid" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := env(t)
			fake := newFakePostHog(t)
			grant(t, c)
			SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
			if tc.name == "invalid_host" {
				// Keep consent bound to the endpoint in use.
				s := LoadState()
				tc.setup(t)
				s.ConsentEndpoint = endpoint
				_ = SaveState(s)
			} else {
				tc.setup(t)
			}
			c.set(at(1, 9, 0))
			if r := MaybeUpload(t.Context()); r.Attempted || fake.hits() != 0 {
				t.Fatalf("%+v", r)
			}
		})
	}
}

func TestDefaultEndpointNeverContactedUnderCI(t *testing.T) {
	c := env(t)
	t.Setenv(EnvPostHogKey, testKey)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	t.Setenv("CI", "1")
	c.set(at(1, 9, 0))
	before := nonLoopbackRequests.Load()
	if r := MaybeUpload(t.Context()); r.Attempted {
		t.Fatalf("%+v", r)
	}
	if nonLoopbackRequests.Load() != before {
		t.Fatal("contacted the default endpoint under CI")
	}
}

func TestLogModeUploadWritesLocallyAndNeverConnects(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	t.Setenv(EnvTelemetry, "log")
	c.set(at(1, 9, 0))
	r := MaybeUpload(t.Context())
	if !r.Sent || fake.hits() != 0 {
		t.Fatalf("%+v hits %d", r, fake.hits())
	}
	p, _ := siblingPath(logFileName)
	data, err := os.ReadFile(p)
	if err != nil || !strings.Contains(string(data), `"session.end"`) || !strings.Contains(string(data), `"api_key"`) {
		t.Fatalf("log file: %s %v", data, err)
	}
	if LoadState().Upload.LastResult != resultLogged {
		t.Fatal("log mode result")
	}
}

func TestDisableWaitsForInFlightUploadThenNothingIsSent(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	fake.mu.Lock()
	fake.block = make(chan struct{})
	release := fake.block
	fake.mu.Unlock()
	done := make(chan UploadResult, 1)
	go func() { done <- MaybeUpload(t.Context()) }()
	for fake.hits() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	disabled := make(chan error, 1)
	go func() { disabled <- Disable("9.9.9", c.now()) }()
	select {
	case <-disabled:
		t.Fatal("disable returned while an upload held the lock")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	<-done
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	if len(spoolBytes(t)) != 0 {
		t.Fatal("disable must delete the spool")
	}
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.add(24 * time.Hour)
	if r := MaybeUpload(t.Context()); r.Attempted || fake.hits() != 1 {
		t.Fatalf("sent after disable: %+v", r)
	}
}

func TestRollupsAreRebuiltWithStableUUIDs(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	for i := 0; i < 3; i++ {
		FeatureUsed("costs", false)
	}
	MessageSent("claude", SendCLI, 500, true)
	Attached("claude", AttachCLI, 10*time.Minute)
	c.set(at(1, 9, 0))
	fake.status.Store(http.StatusInternalServerError)
	MaybeUpload(t.Context())
	c.set(LoadState().Upload.NextTry)
	fake.status.Store(http.StatusOK)
	MaybeUpload(t.Context())
	first, second := fake.batch(t, 0), fake.batch(t, 1)
	ids := func(b phBatch) map[string]string {
		m := map[string]string{}
		for _, ev := range b.Batch {
			if strings.HasSuffix(ev.Event, ".daily") {
				m[ev.Event] = ev.UUID
				if ev.Event == "feature.daily" && ev.Properties["count"] != "2-3" {
					t.Errorf("feature count %v", ev.Properties["count"])
				}
				if ev.Event == "send.daily" && (ev.Properties["len_mode"] != "200-1k" || ev.Properties["queued"] != "1") {
					t.Errorf("send props %v", ev.Properties)
				}
				if ev.Event == "attach.daily" && ev.Properties["total_dur"] != "5-15m" {
					t.Errorf("attach props %v", ev.Properties)
				}
			}
		}
		return m
	}
	a, b := ids(first), ids(second)
	if len(a) != 4 {
		t.Fatalf("rollups %v", a)
	}
	for k, v := range a {
		if b[k] != v {
			t.Fatalf("%s uuid changed on resend: %s vs %s", k, v, b[k])
		}
	}
	if len(LoadState().Daily) != 0 {
		t.Fatal("acknowledged rollup day kept")
	}
}

func TestResetIDRotatesAndDeletesSpool(t *testing.T) {
	c := env(t)
	old := grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	s, err := ResetID()
	if err != nil {
		t.Fatal(err)
	}
	if s.InstallID == old.InstallID || s.Salt == old.Salt || s.Seq != 0 || len(spoolBytes(t)) != 0 || s.Daily != nil {
		t.Fatal("reset-id must rotate id and salt and drop local data")
	}
	if ok, _ := Enabled(LoadState()); !ok {
		t.Fatal("reset-id must keep consent")
	}
}

func TestUninstallIsTheOneSynchronousSend(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	SetProcess("9.9.9", SurfaceCLI)
	if r := SendUninstall(t.Context(), 3, "claude", "bugs"); r.Attempted {
		t.Fatal("uninstall sent without consent")
	}
	grant(t, c)
	r := SendUninstall(t.Context(), 3, "/opt/custom-tool", "free text reason")
	if !r.Sent || fake.hits() != 1 {
		t.Fatalf("%+v", r)
	}
	ev := fake.batch(t, 0).Batch[0]
	if ev.Event != "uninstall" || ev.Properties["last_tool"] != "other" || ev.Properties["reason"] != "skip" || ev.Properties["sessions_total"] != "2-3" {
		t.Fatalf("uninstall event %+v", ev)
	}
}

func TestShowLastKeepsExactBody(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	MaybeUpload(t.Context())
	fake.mu.Lock()
	sent := fake.bodies[0]
	fake.mu.Unlock()
	var a, b any
	_ = json.Unmarshal(sent, &a)
	_ = json.Unmarshal(LoadState().LastPayload, &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatal("show-last differs from the acknowledged body")
	}
}
