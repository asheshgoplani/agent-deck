package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func interactiveEnv(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	for _, k := range append(append([]string{EnvTelemetry, EnvDoNotTrack}, sessionMarkers...), ciMarkers...) {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	prevTTY := isTerminalFn
	isTerminalFn = func() bool { return true }
	prevDisabled := configDisabled
	configDisabled = false
	prevEndpoint := endpoint
	prevNow := nowFn
	t.Cleanup(func() {
		isTerminalFn = prevTTY
		configDisabled = prevDisabled
		endpoint = prevEndpoint
		nowFn = prevNow
	})
}

type receiver struct {
	srv   *httptest.Server
	hits  atomic.Int32
	last  atomic.Pointer[[]byte]
	reply atomic.Int32
}

func newReceiver(t *testing.T) *receiver {
	t.Helper()
	r := &receiver{}
	r.reply.Store(http.StatusNoContent)
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		r.hits.Add(1)
		var buf strings.Builder
		b := make([]byte, 4096)
		n, _ := req.Body.Read(b)
		buf.Write(b[:n])
		body := []byte(buf.String())
		r.last.Store(&body)
		w.WriteHeader(int(r.reply.Load()))
	}))
	t.Cleanup(r.srv.Close)
	endpoint = r.srv.URL + "/v1/ping"
	return r
}

func granted(t *testing.T) *State {
	t.Helper()
	s := LoadState()
	if err := Grant(s, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDefaultStateIsUndecidedAndNeverSends(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	s := LoadState()
	if s.Consent != ConsentUndecided {
		t.Fatalf("fresh state consent = %q, want undecided", s.Consent)
	}
	res := MaybeSend(context.Background(), "9.9.9")
	if res.Attempted || res.Sent || r.hits.Load() != 0 {
		t.Fatalf("undecided state sent: %+v hits=%d", res, r.hits.Load())
	}
}

func TestDeclinedNeverSends(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	s := LoadState()
	Decline(s, "9.9.9", time.Now())
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	MaybeSend(context.Background(), "9.9.9")
	if r.hits.Load() != 0 {
		t.Fatal("declined state sent a payload")
	}
	if LoadState().InstallID != "" {
		t.Fatal("declined state kept an install id")
	}
}

func TestCorruptStateFileIsUndecided(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	path, _ := StatePath()
	os.MkdirAll(strings.TrimSuffix(path, "/"+StateFileName), 0700)
	os.WriteFile(path, []byte(`{"consent":"granted",`), 0600)
	if LoadState().Consent != ConsentUndecided {
		t.Fatal("corrupt file did not resolve to undecided")
	}
	os.WriteFile(path, []byte(`{"consent":"yes","install_id":"abc"}`), 0600)
	if LoadState().Consent != ConsentUndecided {
		t.Fatal("unknown consent value did not resolve to undecided")
	}
	MaybeSend(context.Background(), "9.9.9")
	if r.hits.Load() != 0 {
		t.Fatal("sent from a corrupt/unknown state")
	}
}

func TestHardDisableEnvWinsOverGrantedConsent(t *testing.T) {
	for _, tc := range []struct{ key, val string }{
		{EnvTelemetry, "0"}, {EnvTelemetry, "false"}, {EnvTelemetry, "off"}, {EnvTelemetry, "no"},
		{EnvDoNotTrack, "1"}, {EnvDoNotTrack, "true"}, {EnvDoNotTrack, "yes"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			interactiveEnv(t)
			r := newReceiver(t)
			granted(t)
			t.Setenv(tc.key, tc.val)
			if !HardDisabled() {
				t.Fatal("HardDisabled() = false")
			}
			if ShouldPrompt(defaultState()) {
				t.Fatal("ShouldPrompt true under hard-disable")
			}
			res := MaybeSend(context.Background(), "9.9.9")
			if res.Attempted || r.hits.Load() != 0 {
				t.Fatalf("sent under %s=%s: %+v", tc.key, tc.val, res)
			}
			Record(CounterTUILaunches)
			if len(LoadState().Counters) != 0 {
				t.Fatal("counted under hard-disable")
			}
		})
	}
}

func TestKillSwitchAnyNonOnValueDisables(t *testing.T) {
	for _, v := range []string{"", "disabled", "none", "fasle", "0", "OFF"} {
		t.Run("AGENTDECK_TELEMETRY="+v, func(t *testing.T) {
			interactiveEnv(t)
			t.Setenv(EnvTelemetry, v)
			if r := HardDisableReason(); r != ReasonEnvTelemetry {
				t.Fatalf("reason = %q", r)
			}
		})
	}
	for _, v := range []string{"1", "true", "YES", "on"} {
		t.Run("AGENTDECK_TELEMETRY="+v, func(t *testing.T) {
			interactiveEnv(t)
			t.Setenv(EnvTelemetry, v)
			if HardDisabled() {
				t.Fatal("explicit-on value treated as disable")
			}
			if LoadState().Consent != ConsentUndecided {
				t.Fatal("explicit-on value granted consent")
			}
		})
	}
}

func TestUnreadableConfigFailsClosed(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	granted(t)
	SetConfigUnreadable()
	t.Cleanup(func() { SetConfigDisabled(false) })
	if ok, reason := Enabled(LoadState()); ok || reason != ReasonConfigError {
		t.Fatalf("Enabled = %v %q", ok, reason)
	}
	if MaybeSend(context.Background(), "9.9.9").Attempted || r.hits.Load() != 0 {
		t.Fatal("sent with unreadable config")
	}
	SetConfigDisabled(false) // a later successful parse clears the flag
	if HardDisabled() {
		t.Fatal("flag not cleared")
	}
}

func TestConcurrentRecordDoesNotCorruptState(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			RecordSessionStarted("claude")
		}()
	}
	wg.Wait()
	st := LoadState()
	if st.Consent != ConsentGranted || st.Counters["sessions_started.claude"] != 50 {
		t.Fatalf("after concurrent records: %+v", st)
	}
}

func TestConfigDisabledWinsOverGrantedConsent(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	granted(t)
	SetConfigDisabled(true)
	if MaybeSend(context.Background(), "9.9.9").Attempted || r.hits.Load() != 0 {
		t.Fatal("sent with [telemetry].disabled")
	}
	if ok, reason := Enabled(LoadState()); ok || reason != ReasonConfig {
		t.Fatalf("Enabled = %v %q", ok, reason)
	}
}

func TestEnvOneDoesNotGrantConsent(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	t.Setenv(EnvTelemetry, "1")
	if HardDisabled() {
		t.Fatal("=1 treated as disable")
	}
	res := MaybeSend(context.Background(), "9.9.9")
	if res.Attempted || r.hits.Load() != 0 {
		t.Fatal("AGENTDECK_TELEMETRY=1 caused a send without consent")
	}
	if !ShouldPrompt(LoadState()) {
		t.Fatal("=1 suppressed the prompt; it should be a no-op")
	}
	if LoadState().Consent != ConsentUndecided {
		t.Fatal("=1 changed stored consent")
	}
}

func TestNonInteractiveContextsNeverPromptOrSend(t *testing.T) {
	cases := map[string]func(t *testing.T){
		"no tty":                   func(t *testing.T) { isTerminalFn = func() bool { return false } },
		"CI=true":                  func(t *testing.T) { t.Setenv("CI", "true") },
		"GITHUB_ACTIONS":           func(t *testing.T) { t.Setenv("GITHUB_ACTIONS", "true") },
		"inside a session":         func(t *testing.T) { t.Setenv("AGENTDECK_INSTANCE_ID", "abc123") },
		"inside a session (old)":   func(t *testing.T) { t.Setenv("AGENT_DECK_SESSION_ID", "abc123") },
		"AGENTDECK_NONINTERACTIVE": func(t *testing.T) { t.Setenv("AGENTDECK_NONINTERACTIVE", "1") },
	}
	for name, arrange := range cases {
		t.Run(name, func(t *testing.T) {
			interactiveEnv(t)
			r := newReceiver(t)
			granted(t)
			arrange(t)
			if Interactive() {
				t.Fatal("Interactive() = true")
			}
			if ShouldPrompt(defaultState()) {
				t.Fatal("ShouldPrompt = true in non-interactive context")
			}
			res := MaybeSend(context.Background(), "9.9.9")
			if res.Attempted || r.hits.Load() != 0 {
				t.Fatalf("sent from non-interactive context: %+v", res)
			}
		})
	}
}

func TestCIEqualsFalseIsNotCI(t *testing.T) {
	interactiveEnv(t)
	t.Setenv("CI", "false")
	if !Interactive() {
		t.Fatal("CI=false treated as CI")
	}
}

func TestSendsExactlyOncePerDayWhenGranted(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	granted(t)
	Record(CounterTUILaunches)
	Record(SessionStartedKey("claude"))
	Record(SessionStartedKey("claude"))

	day1 := time.Date(2026, 9, 4, 10, 0, 0, 0, time.UTC)
	nowFn = func() time.Time { return day1 }
	res := MaybeSend(context.Background(), "9.9.9")
	if !res.Sent || r.hits.Load() != 1 {
		t.Fatalf("first send: %+v hits=%d", res, r.hits.Load())
	}
	var p map[string]any
	if err := json.Unmarshal(*r.last.Load(), &p); err != nil {
		t.Fatal(err)
	}
	if p["day"] != "2026-09-04" || p["version"] != "9.9.9" {
		t.Fatalf("payload = %v", p)
	}
	counters := p["counters"].(map[string]any)
	if counters["sessions_started.claude"] != 2.0 || counters["tui_launches"] != 1.0 {
		t.Fatalf("counters = %v", counters)
	}

	nowFn = func() time.Time { return day1.Add(6 * time.Hour) }
	res = MaybeSend(context.Background(), "9.9.9")
	if res.Attempted || r.hits.Load() != 1 {
		t.Fatalf("second send same day: %+v hits=%d", res, r.hits.Load())
	}

	st := LoadState()
	if st.LastSentDay != "2026-09-04" || len(st.Counters) != 0 {
		t.Fatalf("state after send = %+v", st)
	}
	if compact(t, st.LastPayload) != compact(t, *r.last.Load()) {
		t.Fatalf("last_payload not verbatim: %s vs %s", st.LastPayload, *r.last.Load())
	}

	nowFn = func() time.Time { return day1.Add(24 * time.Hour) }
	if res = MaybeSend(context.Background(), "9.9.9"); !res.Sent || r.hits.Load() != 2 {
		t.Fatalf("next-day send: %+v hits=%d", res, r.hits.Load())
	}
}

func TestFailureIsSilentAndNotRetriedSameDay(t *testing.T) {
	interactiveEnv(t)
	r := newReceiver(t)
	r.reply.Store(http.StatusInternalServerError)
	granted(t)
	Record(CounterTUILaunches)
	res := MaybeSend(context.Background(), "9.9.9")
	if !res.Attempted || res.Sent {
		t.Fatalf("failure result = %+v", res)
	}
	r.reply.Store(http.StatusNoContent)
	res = MaybeSend(context.Background(), "9.9.9")
	if res.Attempted || r.hits.Load() != 1 {
		t.Fatalf("retried same day: %+v hits=%d", res, r.hits.Load())
	}
	st := LoadState()
	if st.LastSentDay != "" || st.LastPayload != nil || st.Counters[CounterTUILaunches] != 1 {
		t.Fatalf("failed send mutated state: %+v", st)
	}
}

func TestUnreachableEndpointIsSilent(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	endpoint = DefaultEndpoint // .invalid never resolves
	res := MaybeSend(context.Background(), "9.9.9")
	if res.Sent {
		t.Fatal("sent to .invalid host")
	}
	if res.Attempted {
		t.Fatalf("placeholder must not touch the network, got %+v", res)
	}
}

func TestPayloadHasOnlyAllowlistedKeys(t *testing.T) {
	interactiveEnv(t)
	s := granted(t)
	s.Counters = map[string]int{
		CounterTUILaunches:        3,
		"sessions_started.claude": 1,
		"sessions_started.other":  1,
		"session_title":           1, // hand-edited state: must be dropped
		"path./Users/x":           1,
	}
	body, err := BuildPayload(s, "9.9.9", time.Now()).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{}
	for _, k := range PayloadKeys {
		allowed[k] = true
	}
	for k := range top {
		if !allowed[k] {
			t.Fatalf("unexpected top-level key %q in %s", k, body)
		}
	}
	for _, k := range PayloadKeys {
		if _, ok := top[k]; !ok {
			t.Fatalf("missing key %q", k)
		}
	}
	var counters map[string]int
	json.Unmarshal(top["counters"], &counters)
	allowedCounters := map[string]bool{}
	for _, k := range AllowedCounterKeys() {
		allowedCounters[k] = true
	}
	for k := range counters {
		if !allowedCounters[k] {
			t.Fatalf("counter %q leaked into payload", k)
		}
	}
	if _, ok := counters["session_title"]; ok {
		t.Fatal("forbidden counter survived")
	}
}

func TestPayloadValuesContainNoIdentifiers(t *testing.T) {
	interactiveEnv(t)
	s := granted(t)
	body, _ := BuildPayload(s, "9.9.9", time.Now()).Marshal()
	var top map[string]any
	json.Unmarshal(body, &top)
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	for k, v := range top {
		str, ok := v.(string)
		if !ok {
			continue
		}
		for _, bad := range []string{"/", "\\", "@", ":", "T"} {
			if strings.Contains(str, bad) {
				t.Fatalf("value of %q (%q) contains %q", k, str, bad)
			}
		}
		if host != "" && strings.Contains(str, host) {
			t.Fatalf("value of %q contains hostname", k)
		}
		if user != "" && strings.Contains(str, user) {
			t.Fatalf("value of %q contains username", k)
		}
	}
	if id := top["install_id"].(string); len(id) != 32 {
		t.Fatalf("install_id = %q, want 32 hex chars", id)
	}
	if day := top["day"].(string); len(day) != len(DayFormat) {
		t.Fatalf("day = %q is finer than a day", day)
	}
	for _, forbidden := range []string{"hostname", "user", "path", "title", "prompt", "ip", "time", "session_id"} {
		if _, ok := top[forbidden]; ok {
			t.Fatalf("forbidden field %q present", forbidden)
		}
	}
}

func TestCountersNotRecordedWithoutConsent(t *testing.T) {
	interactiveEnv(t)
	Record(CounterTUILaunches)
	RecordSessionStarted("claude")
	if st := LoadState(); len(st.Counters) != 0 {
		t.Fatalf("undecided install counted: %v", st.Counters)
	}
	if path, _ := StatePath(); fileExists(path) {
		t.Fatal("Record created a state file without consent")
	}
	s := LoadState()
	Decline(s, "9.9.9", time.Now())
	SaveState(s)
	Record(CounterTUILaunches)
	if st := LoadState(); len(st.Counters) != 0 {
		t.Fatalf("declined install counted: %v", st.Counters)
	}
}

func compact(t *testing.T, b []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, b); err != nil {
		t.Fatalf("compact %s: %v", b, err)
	}
	return buf.String()
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func TestUnknownCounterKeyDroppedAndToolNormalised(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	Record("hostname")
	Record("sessions_started.my-secret-project")
	RecordSessionStarted("My Custom Tool")
	RecordSessionStarted("CLAUDE")
	st := LoadState()
	if _, ok := st.Counters["hostname"]; ok {
		t.Fatal("unknown key counted")
	}
	if _, ok := st.Counters["sessions_started.my-secret-project"]; ok {
		t.Fatal("custom tool name counted verbatim")
	}
	if st.Counters["sessions_started.other"] != 1 || st.Counters["sessions_started.claude"] != 1 {
		t.Fatalf("counters = %v", st.Counters)
	}
}

func TestShouldPromptOnlyWhenUndecidedAndInteractive(t *testing.T) {
	interactiveEnv(t)
	if !ShouldPrompt(LoadState()) {
		t.Fatal("fresh interactive install should prompt")
	}
	s := granted(t)
	if ShouldPrompt(s) {
		t.Fatal("prompted after grant")
	}
	Decline(s, "9.9.9", time.Now())
	SaveState(s)
	if ShouldPrompt(LoadState()) {
		t.Fatal("prompted after decline")
	}
	if ShouldPrompt(nil) {
		t.Fatal("nil state prompted")
	}
}

func TestDeclineIsRememberedAcrossVersions(t *testing.T) {
	interactiveEnv(t)
	s := LoadState()
	Decline(s, "1.0.0", time.Now())
	SaveState(s)
	r := newReceiver(t)
	if ShouldPrompt(LoadState()) {
		t.Fatal("new version re-prompted a declined user")
	}
	MaybeSend(context.Background(), "2.0.0")
	if r.hits.Load() != 0 {
		t.Fatal("new version sent for a declined user")
	}
}

func TestDisableRemovesInstallIDAndResetRotates(t *testing.T) {
	interactiveEnv(t)
	s := granted(t)
	first := s.InstallID
	if err := RotateInstallID(s); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == first || len(s.InstallID) != 32 {
		t.Fatalf("rotate: %q -> %q", first, s.InstallID)
	}
	Decline(s, "9.9.9", time.Now())
	if s.InstallID != "" || s.Counters != nil {
		t.Fatalf("decline kept id/counters: %+v", s)
	}
	if err := Grant(s, "9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == first || s.InstallID == "" {
		t.Fatal("re-grant reused an old id")
	}
}

func TestEndpointMustBeHTTPSUnlessLoopback(t *testing.T) {
	for u, wantOK := range map[string]bool{
		"https://example.com/v1/ping":   true,
		"http://127.0.0.1:8787/v1/ping": true,
		"http://localhost:8787/v1/ping": true,
		"http://[::1]:8787/v1/ping":     true,
		"http://example.com/v1/ping":    false,
		"http://10.0.0.5/v1/ping":       false,
		"ftp://example.com/x":           false,
		"file:///etc/passwd":            false,
		"":                              false,
		"https://":                      false,
	} {
		if err := ValidateEndpoint(u); (err == nil) != wantOK {
			t.Errorf("ValidateEndpoint(%q) err=%v, want ok=%v", u, err, wantOK)
		}
	}
}

func TestInvalidConfiguredEndpointNeverSends(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hit = true }))
	defer srv.Close()
	SetEndpoint(strings.Replace(srv.URL, "127.0.0.1", "localtest.me", 1)) // non-loopback http
	res := MaybeSend(context.Background(), "9.9.9")
	if res.Attempted || hit {
		t.Fatalf("sent to plain-http non-loopback endpoint: %+v", res)
	}
	if LoadState().LastAttemptDay != "" {
		t.Fatal("invalid endpoint burned today's attempt")
	}
}

func TestNoRedirectFollowed(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked.Add(1) }))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	endpoint = redirector.URL
	res := MaybeSend(context.Background(), "9.9.9")
	if res.Sent || leaked.Load() != 0 {
		t.Fatalf("redirect followed: %+v leaked=%d", res, leaked.Load())
	}
}

func TestSetEndpointEmptyKeepsDefault(t *testing.T) {
	interactiveEnv(t)
	SetEndpoint("   ")
	if Endpoint() != DefaultEndpoint {
		t.Fatalf("Endpoint() = %q", Endpoint())
	}
	SetEndpoint("https://example.com/p")
	if Endpoint() != "https://example.com/p" {
		t.Fatalf("Endpoint() = %q", Endpoint())
	}
}

func TestPromptTextMentionsEverythingRequired(t *testing.T) {
	txt := PromptText("https://example.com/v1/ping")
	for _, want := range []string{
		"off by default", "https://example.com/v1/ping", "install id", "once per day",
		"agent-deck telemetry disable", "AGENTDECK_TELEMETRY=0", "DO_NOT_TRACK=1",
		"telemetry show-last", DocsURL, "never sent",
	} {
		if !strings.Contains(txt, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	if strings.Contains(txt, "{{") {
		t.Error("unsubstituted placeholder in prompt")
	}
}

func TestStateFileModeAndAtomicity(t *testing.T) {
	interactiveEnv(t)
	granted(t)
	path, _ := StatePath()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Fatalf("state file mode = %o, want 0600", fi.Mode().Perm())
	}
	if fileExists(path + ".tmp") {
		t.Fatal("temp file left behind")
	}
}
