package telemetry

import (
	"bytes"
	"encoding/json"
	"flag"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata goldens")

func TestTestBinaryIsHardOffWithoutSeam(t *testing.T) {
	c := env(t)
	grant(t, c)
	testAllowed = false
	if HardDisableReason() != ReasonTestBinary {
		t.Fatalf("reason = %q, want test binary", HardDisableReason())
	}
	FeatureUsed("fork", false)
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew})
	if len(spoolBytes(t)) != 0 {
		t.Fatal("a test binary recorded an event without EnableForTest")
	}
	if r := MaybeUpload(t.Context()); r.Attempted {
		t.Fatal("a test binary attempted an upload")
	}
	if ShouldPrompt(LoadState()) {
		t.Fatal("a test binary would prompt")
	}
}

// TestConsentGateTable: only granted + no hard off + no CI + TTY records;
// only that + TUI + a later day + a key (and not log mode) uploads.
func TestConsentGateTable(t *testing.T) {
	type stateCase struct {
		name  string
		setup func(t *testing.T, c *clock)
		ok    bool
	}
	states := []stateCase{
		{"none", func(*testing.T, *clock) {}, false},
		{"v1_granted", func(t *testing.T, _ *clock) { writeV1State(t, ConsentGranted) }, false},
		{"v1_declined", func(t *testing.T, _ *clock) { writeV1State(t, ConsentDeclined) }, false},
		{"v2_declined", func(t *testing.T, c *clock) {
			if err := Disable("9.9.9", c.now()); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"granted", func(t *testing.T, c *clock) { grant(t, c) }, true},
		{"stale_endpoint", func(t *testing.T, c *clock) {
			grant(t, c)
			SetEndpoint("https://other.example.com")
		}, false},
	}
	type envCase struct {
		name                   string
		vars                   map[string]string
		record, upload, prompt bool
	}
	envs := []envCase{
		{"clean", nil, true, true, true},
		{"dnt", map[string]string{EnvDoNotTrack: "1"}, false, false, false},
		{"tel_0", map[string]string{EnvTelemetry: "0"}, false, false, false},
		{"tel_off", map[string]string{EnvTelemetry: "off"}, false, false, false},
		{"tel_garbage", map[string]string{EnvTelemetry: "maybe"}, false, false, false},
		{"tel_1", map[string]string{EnvTelemetry: "1"}, true, true, true},
		{"tel_log", map[string]string{EnvTelemetry: "log"}, true, false, false},
		{"ci", map[string]string{"CI": "1"}, false, false, false},
		{"github_actions", map[string]string{"GITHUB_ACTIONS": "true"}, false, false, false},
		// A coding agent at a PTY records as actor=agent under a person's
		// grant, but can never be asked, grant, or upload.
		{"agent_marker", map[string]string{"CLAUDECODE": "1"}, true, false, false},
		{"agent_gemini", map[string]string{"GEMINI_CLI": "1"}, true, false, false},
		{"agent_cursor", map[string]string{"CURSOR_AGENT": "1"}, true, false, false},
		{"agent_codex", map[string]string{"CODEX_THREAD_ID": "t"}, true, false, false},
		{"inside_session", map[string]string{"AGENTDECK_INSTANCE_ID": "x"}, true, false, false},
	}
	for _, st := range states {
		for _, e := range envs {
			for _, tty := range []bool{true, false} {
				name := st.name + "/" + e.name
				if !tty {
					name += "/notty"
				}
				t.Run(name, func(t *testing.T) {
					c := env(t)
					fake := newFakePostHog(t)
					st.setup(t, c)
					for k, v := range e.vars {
						t.Setenv(k, v)
					}
					isTerminalFn = func() bool { return tty }
					if st.name == "none" {
						if want := e.prompt && tty; ShouldPrompt(LoadState()) != want {
							t.Fatalf("prompt=%v want %v", !want, want)
						}
					}
					SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, SessionID: "s1"})
					recorded := len(spoolLines(t)) > 0
					if want := st.ok && e.record && tty; recorded != want {
						t.Fatalf("recorded=%v want %v", recorded, want)
					}
					c.set(at(1, 9, 0))
					MaybeUpload(t.Context())
					if want := st.ok && e.upload && tty; (fake.hits() > 0) != want {
						t.Fatalf("uploaded=%v want %v", fake.hits() > 0, want)
					}
				})
			}
		}
	}
}

func TestShouldPrompt(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, c *clock)
		want  bool
	}{
		{"undecided", func(*testing.T, *clock) {}, true},
		{"v1_granted_is_asked_again", func(t *testing.T, _ *clock) { writeV1State(t, ConsentGranted) }, true},
		{"v1_declined_asked_once", func(t *testing.T, _ *clock) { writeV1State(t, ConsentDeclined) }, true},
		{"v2_declined", func(t *testing.T, c *clock) { _ = Disable("9.9.9", c.now()) }, false},
		{"v1_declined_then_v2_declined", func(t *testing.T, c *clock) {
			writeV1State(t, ConsentDeclined)
			s := LoadState()
			Decline(s, "9.9.9", c.now())
			if err := SaveState(s); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"granted", func(t *testing.T, c *clock) { grant(t, c) }, false},
		{"log_mode", func(t *testing.T, _ *clock) { t.Setenv(EnvTelemetry, "log") }, false},
		{"dnt", func(t *testing.T, _ *clock) { t.Setenv(EnvDoNotTrack, "true") }, false},
		{"ci", func(t *testing.T, _ *clock) { t.Setenv("BUILDKITE", "true") }, false},
		{"inside_session", func(t *testing.T, _ *clock) { t.Setenv("AGENT_DECK_SESSION_ID", "x") }, false},
		{"claude_code", func(t *testing.T, _ *clock) { t.Setenv("CLAUDECODE", "1") }, false},
		{"gemini_cli", func(t *testing.T, _ *clock) { t.Setenv("GEMINI_CLI", "1") }, false},
		{"cursor_agent", func(t *testing.T, _ *clock) { t.Setenv("CURSOR_AGENT", "1") }, false},
		{"codex_sandbox", func(t *testing.T, _ *clock) { t.Setenv("CODEX_SANDBOX", "seatbelt") }, false},
		{"not_a_tty", func(*testing.T, *clock) { isTerminalFn = func() bool { return false } }, false},
		{"config_disabled", func(*testing.T, *clock) { SetConfigDisabled(true) }, false},
		{"config_unreadable", func(*testing.T, *clock) { SetConfigUnreadable() }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := env(t)
			tc.setup(t, c)
			if got := ShouldPrompt(LoadState()); got != tc.want {
				t.Fatalf("ShouldPrompt = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRegrantAfterEndpointChangeDropsOldSpool: events recorded under one
// consent and destination never reach another one under a new install id.
func TestRegrantAfterEndpointChangeDropsOldSpool(t *testing.T) {
	c := env(t)
	old := grant(t, c)
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, SessionID: "s1"})
	if len(spoolLines(t)) == 0 {
		t.Fatal("setup: nothing spooled")
	}
	fake := newFakePostHog(t) // a different endpoint: the old grant is stale
	s := grant(t, c)
	if s.InstallID == old.InstallID {
		t.Fatal("a new endpoint must mint a new install id")
	}
	if n := len(spoolBytes(t)); n != 0 {
		t.Fatalf("spool kept %d bytes across re-consent", n)
	}
	c.set(at(1, 9, 0))
	MaybeUpload(t.Context())
	for i := 0; i < fake.hits(); i++ {
		for _, e := range fake.batch(t, i).Batch {
			if e.Event == "session.create" {
				t.Fatal("an event recorded under the old consent was uploaded")
			}
		}
	}
}

// TestWebEventsCarryTheWebSurface: a TUI process also serves the web UI;
// sessions created or ended through it are surface=web.
func TestWebEventsCarryTheWebSurface(t *testing.T) {
	c := env(t) // process surface: tui
	grant(t, c)
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaWeb, SessionID: "w1"})
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop, SessionID: "w1", Surface: SurfaceWeb})
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, SessionID: "t1"})
	want := []string{"web", "web", "tui"}
	var got []string
	for _, l := range spoolLines(t) {
		if l.E == "session.create" || l.E == "session.end" {
			got = append(got, l.SF)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("surfaces = %v, want %v", got, want)
	}
}

// TestJSONCommandsCheckStderrForTheTerminal: with --json stdout is piped
// output, so the TTY check (which also gates recording the consent events)
// must look at stderr.
func TestJSONCommandsCheckStderrForTheTerminal(t *testing.T) {
	prev := terminalOut
	t.Cleanup(func() { terminalOut = prev })
	if terminalOut != os.Stdout {
		t.Fatal("default output check must be stdout")
	}
	UseStderrForTerminalCheck()
	if terminalOut != os.Stderr {
		t.Fatal("--json must check stderr")
	}
}

func TestV1StateMigration(t *testing.T) {
	c := env(t)
	writeV1State(t, ConsentGranted)
	s := LoadState()
	if s.Consent != ConsentUndecided || s.Previous() != "v1_granted" {
		t.Fatalf("v1 grant: consent=%s previous=%s", s.Consent, s.Previous())
	}
	if ok, _ := Enabled(s); ok {
		t.Fatal("a v1 grant enables v2")
	}
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == strings.Repeat("a", 32) || s.Counters != nil || len(s.Salt) != 64 {
		t.Fatal("v2 grant must mint a new id and salt and drop v1 counters")
	}
	if !s.PreV2 {
		t.Fatal("an install with v1 state is pre_v2")
	}

	writeV1State(t, ConsentDeclined)
	s = LoadState()
	if !s.V1Declined() || s.Previous() != "v1_declined" {
		t.Fatal("v1 decline not recognised")
	}
	Decline(s, "9.9.9", c.now())
	if s.V1Declined() || s.DeclinedSchema != SchemaVersion {
		t.Fatal("a v2 decline must be final")
	}
}

func TestLogModeWithoutConsentWritesRecordedFalseAndNeverGrants(t *testing.T) {
	env(t)
	t.Setenv(EnvTelemetry, "log")
	if HardDisabled() {
		t.Fatal("log mode is not a hard off")
	}
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaCLIAdd})
	p, _ := siblingPath(logFileName)
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("log mode wrote nothing: %v", err)
	}
	if !bytes.Contains(data, []byte(`"recorded":false`)) || !bytes.Contains(data, []byte(`"session.create"`)) {
		t.Fatalf("log line = %s", data)
	}
	if LoadState().Consent != ConsentUndecided || len(spoolBytes(t)) != 0 {
		t.Fatal("log mode granted or spooled")
	}
}

func TestBucketEdges(t *testing.T) {
	m, h, d := time.Minute, time.Hour, 24*time.Hour
	checks := []struct {
		got, want string
	}{
		{CountBucket(-1), "0"}, {CountBucket(0), "0"}, {CountBucket(1), "1"}, {CountBucket(2), "2-3"},
		{CountBucket(3), "2-3"}, {CountBucket(4), "4-7"}, {CountBucket(7), "4-7"}, {CountBucket(8), "8-15"},
		{CountBucket(15), "8-15"}, {CountBucket(16), "16-31"}, {CountBucket(31), "16-31"}, {CountBucket(32), "32-63"},
		{CountBucket(63), "32-63"}, {CountBucket(64), "64+"}, {CountBucket(100000), "64+"},
		{DurBucket(-m), "<1m"}, {DurBucket(m - 1), "<1m"}, {DurBucket(m), "1-5m"}, {DurBucket(5*m - 1), "1-5m"},
		{DurBucket(5 * m), "5-15m"}, {DurBucket(15 * m), "15-60m"}, {DurBucket(h - 1), "15-60m"}, {DurBucket(h), "1-4h"},
		{DurBucket(4 * h), "4-24h"}, {DurBucket(d - 1), "4-24h"}, {DurBucket(d), "1-7d"}, {DurBucket(7*d - 1), "1-7d"},
		{DurBucket(7 * d), "7d+"},
		{SinceBucket(0), "<1m"}, {SinceBucket(m), "1-5m"}, {SinceBucket(5 * m), "5-30m"}, {SinceBucket(30*m - 1), "5-30m"},
		{SinceBucket(30 * m), "30m-1d"}, {SinceBucket(d), "1-7d"}, {SinceBucket(7 * d), "7d+"},
		{LenBucket(0), "<50"}, {LenBucket(49), "<50"}, {LenBucket(50), "50-200"}, {LenBucket(199), "50-200"},
		{LenBucket(200), "200-1k"}, {LenBucket(999), "200-1k"}, {LenBucket(1000), "1k+"},
		{MSBucket(49 * time.Millisecond), "<50"}, {MSBucket(50 * time.Millisecond), "50-100"},
		{MSBucket(100 * time.Millisecond), "100-250"}, {MSBucket(250 * time.Millisecond), "250-500"},
		{MSBucket(500 * time.Millisecond), "500-1000"}, {MSBucket(time.Second), "1-2s"},
		{MSBucket(2 * time.Second), "2-5s"}, {MSBucket(5 * time.Second), "5s+"},
		{RatingBucket(1), "1-2"}, {RatingBucket(2), "1-2"}, {RatingBucket(3), "3"}, {RatingBucket(4), "4-5"}, {RatingBucket(5), "4-5"},
		{AgeBucket(0), "d0"}, {AgeBucket(1), "d1"}, {AgeBucket(2), "d2-7"}, {AgeBucket(7), "d2-7"}, {AgeBucket(8), "d8-30"},
		{AgeBucket(30), "d8-30"}, {AgeBucket(31), "d31-90"}, {AgeBucket(90), "d31-90"}, {AgeBucket(91), "d91+"},
	}
	for i, c := range checks {
		if c.got != c.want {
			t.Errorf("check %d: got %q want %q", i, c.got, c.want)
		}
	}
	if ToolBit("claude") != 1 || ToolBit("shell") != 1<<11 || ToolBit("my-tool") != 1<<31 || ToolBit(" Codex ") != 2 {
		t.Fatal("tool bitmask order changed")
	}
	if HourBit(23) != 1<<23 || HourBit(24) != 0 || HourBit(-1) != 0 {
		t.Fatal("hour mask")
	}
}

func TestSchemaIsSelfConsistent(t *testing.T) {
	if len(Events) != 29 {
		t.Fatalf("schema 2 publishes 29 events, have %d", len(Events))
	}
	seen := map[string]bool{}
	now := 0
	for _, e := range Events {
		if seen[e.Name] {
			t.Fatalf("duplicate event %s", e.Name)
		}
		seen[e.Name] = true
		if e.Ships == shipsNow {
			now++
		}
		keys := map[string]bool{}
		for _, p := range e.Props {
			if keys[p.Key] {
				t.Fatalf("%s: duplicate property %s", e.Name, p.Key)
			}
			keys[p.Key] = true
			switch p.Kind {
			case KindEnum:
				if len(p.Values) == 0 {
					t.Fatalf("%s.%s: empty enum", e.Name, p.Key)
				}
			case KindBucket:
				if len(bucketLabels[p.Bucket]) == 0 {
					t.Fatalf("%s.%s: unknown bucket", e.Name, p.Key)
				}
			}
		}
	}
	if now != 16 {
		t.Fatalf("%d events ship call sites in 1.16.18, want 16", now)
	}
	for _, f := range FeatureValues {
		if strings.ContainsAny(f, " /.") {
			t.Fatalf("feature %q is not an identifier", f)
		}
	}
}

func TestValidateRejectsAnythingOutsideTheSchema(t *testing.T) {
	good := map[string]any{"feature": "fork", "count": "1", "errors": "0"}
	if err := Validate("feature.daily", good); err != nil {
		t.Fatal(err)
	}
	bad := []struct {
		name  string
		props map[string]any
	}{
		{"nope.event", map[string]any{}},
		{"feature.daily", map[string]any{"feature": "fork", "path": "/tmp"}},
		{"feature.daily", map[string]any{"feature": "my secret feature"}},
		{"feature.daily", map[string]any{"count": "5"}},
		{"feature.daily", map[string]any{"count": 5}},
		{"session.create", map[string]any{"worktree": "true"}},
		{"session.create", map[string]any{"ds_session": "not-hex-at-all!!"}},
		{"session.create", map[string]any{"ds_session": strings.Repeat("A", 16)}},
		{"activity.hourly", map[string]any{"tools_running": -1}},
		{"usage.daily", map[string]any{"human_hours": 1 << 24}},
		{"update", map[string]any{"from_minor": "1.16; rm -rf"}},
		{"env.snapshot", map[string]any{"tmux_minor": 3.4}},
	}
	for i, b := range bad {
		if Validate(b.name, b.props) == nil {
			t.Errorf("case %d accepted: %s %v", i, b.name, b.props)
		}
	}
}

// recordEveryEvent drives one of each tier-1 event through public helpers.
func recordEveryEvent(t *testing.T, c *clock) {
	t.Helper()
	fleet := FleetCounts{Sessions: 5, Groups: 2, Profiles: 1, Conductors: 1}
	AfterConsent(SourceTUIFirstRun, "none", Baseline{InstallMethod: "brew", TmuxOK: true, ToolsFound: ToolMask("claude", "codex"), HadConfig: true}, &fleet)
	TUIStarted(fleet, false)
	EnvSnapshot(EnvInfo{Terminal: "ghostty", TmuxMinor: "3.4", Shell: "zsh", InstallMethod: "brew", Color: "truecolor", ToolsInstalled: ToolMask("claude"), ConfigSections: ConfigSectionMask("claude", "tmux")})
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, Worktree: true, MCPs: 2, InGroup: true, SessionID: "sess-1"})
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaCLIAdd, SessionID: "sess-2"})
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop, Lifetime: 90 * time.Minute, SessionID: "sess-1"})
	MessageSent("claude", SendCLI, 120, false)
	Attached("codex", AttachTUI, 3*time.Minute)
	ErrorOccurred(AreaTmux, KindTmuxTooOld, "")
	UpdateAttempted("1.16.17", "1.16.18", UpdateManual, UpdateOK, false)
	FeatureUsed("mcp_attach", false)
	CLICommand("costs")
	sp := &Sampler{now: c.now, emit: func(p map[string]any, at time.Time) { recordAt("activity.hourly", p, "", at) }}
	sp.Observe(func() []SessionSample {
		return []SessionSample{{Tool: "claude", Status: StatusRunning}, {Tool: "codex", Status: StatusIdle}}
	})
	sp.KeyPressed()
	sp.Close()
	TUIExited(2*time.Hour, ExitQuit)
}

// TestAllowListGolden: every tier-1 event, encoded as the PostHog batch the
// next upload would send, equals testdata/batch.golden.json, and every
// property of every event validates against the schema.
func TestAllowListGolden(t *testing.T) {
	c := env(t)
	sequentialUUIDs(t)
	t.Setenv(EnvPostHogKey, testKey)
	grant(t, c)
	s := LoadState()
	s.InstallID = "8f1c2a9b4d6e7f0011223344556677aa"
	s.Salt = strings.Repeat("5a", 32)
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	recordEveryEvent(t, c)
	c.set(at(1, 9, 0))
	bodies, err := PreviewBatch()
	if err != nil || len(bodies) != 1 {
		t.Fatalf("preview: %d bodies, err %v", len(bodies), err)
	}
	var batch phBatch
	if err := json.Unmarshal(bodies[0], &batch); err != nil {
		t.Fatal(err)
	}
	envelope := map[string]bool{"$process_person_profile": true, "$geoip_disable": true, "$lib": true}
	for _, p := range Envelope {
		envelope[p.Key] = true
	}
	names := map[string]bool{}
	for _, ev := range batch.Batch {
		names[ev.Event] = true
		props := map[string]any{}
		for k, v := range ev.Properties {
			if !envelope[k] {
				props[k] = v
			}
		}
		if err := Validate(ev.Event, props); err != nil {
			t.Errorf("%s: %v", ev.Event, err)
		}
		for _, k := range []string{"$ip", "$set", "$set_once", "$groups", "$session_id", "$current_url"} {
			if _, ok := ev.Properties[k]; ok {
				t.Errorf("%s carries %s", ev.Event, k)
			}
		}
		if ev.Properties["$process_person_profile"] != false || ev.Properties["$geoip_disable"] != true {
			t.Errorf("%s is not personless/geoip-off", ev.Event)
		}
		ev.Properties["os"], ev.Properties["arch"] = "<os>", "<arch>"
	}
	for _, e := range Events {
		if e.Tier == 1 && e.Name != "uninstall" && !names[e.Name] {
			t.Errorf("golden batch lacks %s", e.Name)
		}
	}
	got, _ := json.MarshalIndent(batch, "", "  ")
	golden := filepath.Join("testdata", "batch.golden.json")
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, append(got, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to create)", err)
	}
	if string(bytes.TrimSpace(want)) != string(got) {
		t.Fatalf("batch differs from %s (run with -update after reviewing):\n%s", golden, got)
	}
}

// TestRedactionCanaries feeds identifying strings into every helper and
// checks none of their bytes reach the spool, the batch or the log.
func TestRedactionCanaries(t *testing.T) {
	canaries := []string{
		"/Users/alice-canary/src/secret-repo", "alice.canary@example.com", "build-host-canary-7.internal",
		"Fix the canary login bug in payments", "apikey-canary-0123456789", "my-canary-mcp-server",
		"../../etc/canary-passwd", "feat/canary-branch", "canary title with spaces",
	}
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 20; i++ {
		b := make([]byte, 24)
		for j := range b {
			b[j] = byte('a' + rng.Intn(26))
		}
		canaries = append(canaries, "rnd"+string(b))
	}
	c := env(t)
	t.Setenv(EnvPostHogKey, testKey)
	grant(t, c)
	for _, cn := range canaries {
		SessionCreated(SessionCreateInfo{Tool: cn, Via: CreateVia(cn), SessionID: cn})
		SessionEnded(SessionEndInfo{Tool: cn, Kind: EndKind(cn), SessionID: cn})
		MessageSent(cn, SendVia(cn), len(cn), true)
		Attached(cn, AttachVia(cn), time.Minute)
		ErrorOccurred(ErrArea(cn), ErrKind(cn), cn)
		ErrorOccurred(AreaConfig, KindOther, cn)
		FeatureUsed(Feature(cn), false)
		CLICommand(Feature(cn))
		UpdateAttempted(cn, cn, UpdateKind(cn), UpdateOutcome(cn), true)
		EnvSnapshot(EnvInfo{Terminal: cn, TmuxMinor: cn, Shell: cn, InstallMethod: cn, Color: cn})
		c.add(time.Hour)
	}
	c.add(48 * time.Hour)
	bodies, err := PreviewBatch()
	if err != nil {
		t.Fatal(err)
	}
	all := append([]byte{}, spoolBytes(t)...)
	for _, b := range bodies {
		all = append(all, b...)
	}
	if len(bodies) == 0 {
		t.Fatal("nothing was recorded; the canary test proves nothing")
	}
	for _, cn := range canaries {
		if bytes.Contains(all, []byte(cn)) {
			t.Errorf("canary %q leaked", cn)
		}
	}
}

func TestFunnelMilestonesFireOnceInOrder(t *testing.T) {
	c := env(t)
	grant(t, c)
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew})
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew})
	c.set(at(1, 10, 0))
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaTUIFork, Worktree: true})
	SessionCreated(SessionCreateInfo{Tool: "codex", Via: ViaTUINew})
	Attached("claude", AttachTUI, time.Minute)
	Attached("claude", AttachTUI, time.Minute)
	MessageSent("claude", SendCLI, 10, false)
	FeatureUsed("mcp_attach", false)
	FeatureUsed("group_create", true) // failed: no milestone
	SessionRunning("claude")
	SessionRunning("claude")
	var steps []string
	for _, l := range spoolLines(t) {
		if l.E == "onboard.milestone" {
			steps = append(steps, l.P["step"].(string))
		}
	}
	want := []string{"first_session_created", "second_session", "second_tool", "first_fork", "first_worktree",
		"activated", "first_attach", "first_send", "first_mcp_attach", "first_session_running"}
	if !reflect.DeepEqual(steps, want) {
		t.Fatalf("milestones = %v\nwant %v", steps, want)
	}
}

func TestAfterConsentRecordsConsentBaselineAndFirstRun(t *testing.T) {
	c := env(t)
	writeV1State(t, ConsentGranted)
	s := LoadState()
	prev := s.Previous()
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	AfterConsent(SourceTUIFirstRun, prev, Baseline{Sessions: 3, ToolsUsed: []string{"claude", "codex"}, HasGroups: true}, &FleetCounts{Sessions: 3})
	lines := spoolLines(t)
	got := eventNames(lines)
	want := []string{"telemetry.consent", "onboard.baseline", "app.start", "onboard.milestone:first_run", "onboard.milestone:consented"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %v\nwant %v", got, want)
	}
	if lines[0].P["previous"] != "v1_granted" || lines[0].P["source"] != "tui_first_run" {
		t.Fatalf("consent props %v", lines[0].P)
	}
	before := lines[1].P["milestones_before"].(int)
	for _, st := range []step{stepFirstRun, stepFirstCreated, stepSecondSession, stepSecondTool, stepFirstGroup} {
		if before&int(milestoneBit(st)) == 0 {
			t.Errorf("milestones_before lacks %s", milestoneNames[st])
		}
	}
	if lines[2].P["start_kind"] == "first_ever" {
		t.Fatal("an upgrader's first v2 start is not first_ever")
	}
	if lines[3].P["before_consent"] != true {
		t.Fatal("first_run happened before consent")
	}
	// Steps the install had already reached do not fire again.
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew})
	if n := countEvent(spoolLines(t), "onboard.milestone"); n != 2 {
		t.Fatalf("%d milestones; pre-consent steps fired again", n)
	}
}

func TestDailyCapDropsAndCounts(t *testing.T) {
	c := env(t)
	grant(t, c)
	for i := 0; i < dailyEventCap+5; i++ {
		SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	}
	if n := len(spoolLines(t)); n != dailyEventCap {
		t.Fatalf("spooled %d, cap %d", n, dailyEventCap)
	}
	emitted, dropped := CapUsage(LoadState(), c.now())
	if emitted != dailyEventCap || dropped != 5 {
		t.Fatalf("cap usage %d/%d", emitted, dropped)
	}
}

func TestErrorDedupPerHourAndDailyCap(t *testing.T) {
	c := env(t)
	grant(t, c)
	ErrorOccurred(AreaTmux, KindTmuxMissing, "")
	ErrorOccurred(AreaTmux, KindTmuxMissing, "")
	c.add(time.Hour)
	ErrorOccurred(AreaTmux, KindTmuxMissing, "")
	if n := countEvent(spoolLines(t), "error"); n != 2 {
		t.Fatalf("%d error events, want 2 (deduped per hour)", n)
	}
	for i := 0; i < 30; i++ {
		ErrorOccurred(AreaSend, errorKindsForTest[i%len(errorKindsForTest)], "")
		c.add(time.Minute * 2)
	}
	if n := countEvent(spoolLines(t), "error"); n > dailyErrorCap {
		t.Fatalf("%d error events, cap %d", n, dailyErrorCap)
	}
	l := spoolLines(t)[0]
	if l.P["before_first_success"] != true || l.P["onboarding_step"] != "none" {
		t.Fatalf("error props %v", l.P)
	}
}

var errorKindsForTest = []ErrKind{KindTimeout, KindPermission, KindDiskFull, KindOther, KindDBLocked, KindConfigParse, KindToolAuth}

func TestBasicLevelRecordsOnlyBasicEventsWithoutHours(t *testing.T) {
	c := env(t)
	grant(t, c)
	if _, err := SetLevel(LevelBasic); err != nil {
		t.Fatal(err)
	}
	SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, SessionID: "x"})
	TUIStarted(FleetCounts{Sessions: 1}, true)
	FeatureUsed("fork", false)
	lines := spoolLines(t)
	if len(lines) != 1 || lines[0].E != "app.start" || lines[0].H != nil || lines[0].W != nil {
		t.Fatalf("basic level spooled %v", eventNames(lines))
	}
	c.set(at(1, 9, 0))
	t.Setenv(EnvPostHogKey, testKey)
	bodies, _ := PreviewBatch()
	var b phBatch
	_ = json.Unmarshal(bodies[0], &b)
	for _, ev := range b.Batch {
		if ev.Event != "app.start" && ev.Event != "usage.daily" {
			t.Fatalf("basic level uploads %s", ev.Event)
		}
		if !strings.HasSuffix(ev.Timestamp, "T12:00:00Z") {
			t.Fatalf("basic timestamp %s", ev.Timestamp)
		}
		if _, ok := ev.Properties["hour_local"]; ok {
			t.Fatal("basic level sends hour_local")
		}
	}
	// Config can lower the level but never raise it.
	SetConfigLevel("full")
	if EffectiveLevel(LoadState()) != LevelBasic {
		t.Fatal("config raised the level")
	}
	s, _ := SetLevel(LevelFull)
	SetConfigLevel("basic")
	if EffectiveLevel(s) != LevelBasic {
		t.Fatal("config basic did not lower the level")
	}
}

func TestSamplerOneSamplerPerMachineAndHourlyEvent(t *testing.T) {
	c := env(t)
	grant(t, c)
	sp := NewSampler()
	if sp == nil {
		t.Fatal("first TUI must win the sampler lock")
	}
	if NewSampler() != nil {
		t.Fatal("a second TUI must not sample")
	}
	samples := func(run, wait int) func() []SessionSample {
		return func() []SessionSample {
			var out []SessionSample
			for i := 0; i < run; i++ {
				out = append(out, SessionSample{Tool: "claude", Status: StatusRunning})
			}
			for i := 0; i < wait; i++ {
				out = append(out, SessionSample{Tool: "codex", Status: StatusWaiting})
			}
			return out
		}
	}
	sp.now = c.now
	sp.Observe(samples(1, 0))
	c.add(30 * time.Second)
	sp.Observe(samples(9, 9)) // inside the minute: not sampled
	c.add(time.Minute)
	sp.Observe(samples(3, 1))
	sp.KeyPressed()
	c.set(at(0, 15, 1))
	sp.Observe(samples(0, 0)) // hour change emits 14:00
	var hourly []spoolLine
	for _, l := range spoolLines(t) {
		if l.E == "activity.hourly" {
			hourly = append(hourly, l)
		}
	}
	if len(hourly) != 1 {
		t.Fatalf("%d hourly events", len(hourly))
	}
	p := hourly[0].P
	if p["running"] != "2-3" || p["waiting"] != "1" || p["sampled_min"] != "2-3" || p["human_active"] != true || *hourly[0].H != 14 {
		t.Fatalf("hourly = %v hour %d", p, *hourly[0].H)
	}
	if p["tools_running"] != int(ToolBit("claude")) {
		t.Fatalf("tools_running %v", p["tools_running"])
	}
	sp.Close()
	if NewSampler() == nil {
		t.Fatal("lock not released on Close")
	}
}

func hourlyLines(t *testing.T) []spoolLine {
	t.Helper()
	var out []spoolLine
	for _, l := range spoolLines(t) {
		if l.E == "activity.hourly" {
			out = append(out, l)
		}
	}
	return out
}

func running(n int) func() []SessionSample {
	return func() []SessionSample {
		out := make([]SessionSample, n)
		for i := range out {
			out[i] = SessionSample{Tool: "claude", Status: StatusRunning}
		}
		return out
	}
}

// TestSamplerResetOnGrantDropsPreConsentMinutes: minutes observed before a
// mid-hour grant never reach that hour's activity.hourly.
func TestSamplerResetOnGrantDropsPreConsentMinutes(t *testing.T) {
	c := env(t)
	sp := &Sampler{now: c.now, emit: func(p map[string]any, at time.Time) { recordAt("activity.hourly", p, "", at) }}
	for i := 0; i < 5; i++ {
		sp.Observe(running(9))
		sp.KeyPressed()
		c.add(time.Minute)
	}
	grant(t, c)
	sp.Reset()
	sp.Observe(running(1))
	c.set(at(0, 15, 1))
	sp.Observe(running(0))
	hourly := hourlyLines(t)
	if len(hourly) != 1 {
		t.Fatalf("%d hourly events", len(hourly))
	}
	if p := hourly[0].P; p["running"] != "1" || p["sampled_min"] != "1" || p["human_active"] != false {
		t.Fatalf("pre-consent activity leaked into the hour: %v", p)
	}
}

// TestSamplerUsesLocalHourBoundaries: in a UTC+5:30 zone an hour runs from
// local :00 to :00, not from :30 to :30.
func TestSamplerUsesLocalHourBoundaries(t *testing.T) {
	prev := time.Local
	time.Local = time.FixedZone("UTC+0530", 5*3600+1800)
	t.Cleanup(func() { time.Local = prev })
	c := env(t)
	grant(t, c)
	sp := &Sampler{now: c.now, emit: func(p map[string]any, at time.Time) { recordAt("activity.hourly", p, "", at) }}
	c.set(at(1, 14, 10))
	sp.Observe(running(1))
	c.set(at(1, 14, 50))
	sp.Observe(running(1))
	c.set(at(1, 15, 5))
	sp.Observe(running(0))
	hourly := hourlyLines(t)
	if len(hourly) != 1 || *hourly[0].H != 14 || hourly[0].P["sampled_min"] != "2-3" {
		for _, l := range hourly {
			t.Logf("hour %d %v", *l.H, l.P)
		}
		t.Fatalf("want one 14:00 hour with both samples, got %d event(s)", len(hourly))
	}
}

// TestSamplerCloseRacesObserve: the exit path closes the sampler from the
// signal goroutine while the TUI goroutine may be observing (run with -race).
func TestSamplerCloseRacesObserve(t *testing.T) {
	c := env(t)
	grant(t, c)
	sp := &Sampler{now: c.now, emit: func(map[string]any, time.Time) {}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			sp.Observe(running(1))
			sp.KeyPressed()
		}
	}()
	for i := 0; i < 200; i++ {
		sp.Close()
	}
	<-done
}

// TestRemotePathsNeverTouchTelemetryState: remote add/update/sweep code must
// not read, write or copy telemetry-state.json (consent is per machine).
func TestRemotePathsNeverTouchTelemetryState(t *testing.T) {
	var files []string
	for _, pattern := range []string{"../session/remote*.go", "../../cmd/agent-deck/remote*.go"} {
		m, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, m...)
	}
	if len(files) < 3 {
		t.Fatalf("found only %d remote source files; the glob is stale", len(files))
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, needle := range []string{"telemetry-state", "telemetry-spool", "internal/telemetry", "StateFileName"} {
			if bytes.Contains(data, []byte(needle)) {
				t.Errorf("%s references %s", f, needle)
			}
		}
	}
}

func TestHTTPClientIsHardened(t *testing.T) {
	if httpClient.CheckRedirect == nil || httpClient.Jar != nil || httpClient.Timeout != sendTimeout {
		t.Fatal("client must refuse redirects, keep no cookies and time out")
	}
	tr := httpClient.Transport.(loopbackOnly).next.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatal("proxy environment must be ignored")
	}
}

func TestEndpointValidation(t *testing.T) {
	for _, ok := range []string{"https://eu.i.posthog.com", "https://t.example.com/ingest", "http://127.0.0.1:8080", "http://localhost:1"} {
		if err := ValidateEndpoint(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"http://example.com", "ftp://x", "https://user:pw@x.com", "https://x.com/?q=1", "https://x.com/#f", "https://"} {
		if ValidateEndpoint(bad) == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	env(t)
	SetEndpoint("")
	if Endpoint() != DefaultEndpoint || batchURL() != "https://eu.i.posthog.com/batch/" {
		t.Fatalf("default endpoint %s / %s", Endpoint(), batchURL())
	}
	SetEndpoint(" https://ingest.example.com/ ")
	if batchURL() != "https://ingest.example.com/batch/" {
		t.Fatal(batchURL())
	}
}

func TestPromptTextFitsAndDisclosesDestination(t *testing.T) {
	text := PromptText(DefaultEndpoint)
	for _, want := range []string{"a few times a day to PostHog (EU)", "agent-deck telemetry preview", "agent-deck telemetry off", "DO_NOT_TRACK=1", "IP addresses are discarded"} {
		if !strings.Contains(text, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	for _, l := range strings.Split(text, "\n") {
		if n := len([]rune(l)); n > PromptWidth-6 {
			t.Errorf("line too wide (%d): %q", n, l)
		}
	}
	if !strings.Contains(PromptText("https://ingest.example.com"), "https://ingest.example.com") {
		t.Fatal("a custom endpoint must be shown")
	}
	if PromptFits("https://" + strings.Repeat("x", 80) + ".com") {
		t.Fatal("an endpoint that does not fit must refuse acceptance")
	}
}

func TestPostHogKeyPrecedenceAndValidation(t *testing.T) {
	env(t)
	if Configured() || PostHogKeySource() != KeySourceNone {
		t.Fatal("no key must mean not configured")
	}
	SetPostHogKey("phc_fromconfig0123456789ab")
	if k, ok := PostHogKey(); !ok || k != "phc_fromconfig0123456789ab" || PostHogKeySource() != KeySourceConfig {
		t.Fatal("config key")
	}
	t.Setenv(EnvPostHogKey, testKey)
	if k, _ := PostHogKey(); k != testKey || PostHogKeySource() != KeySourceEnv {
		t.Fatal("env must win over config")
	}
	t.Setenv(EnvPostHogKey, "not a key")
	if Configured() {
		t.Fatal("a malformed key must not configure uploads")
	}
	t.Setenv("POSTHOG_API_KEY", "phc_foreignkey0123456789ab")
	t.Setenv(EnvPostHogKey, "")
	SetPostHogKey("")
	if Configured() {
		t.Fatal("POSTHOG_* environment must have no effect")
	}

	const compiled = "phc_compiledin0123456789ab"
	prev := defaultPostHogKey
	defaultPostHogKey = compiled
	t.Cleanup(func() { defaultPostHogKey = prev })
	t.Setenv(EnvPostHogKey, testKey)
	SetPostHogKey("phc_fromconfig0123456789ab")
	if k, ok := PostHogKey(); !ok || k != compiled {
		t.Fatalf("key = %q: env or config must not override a compiled-in key", k)
	}
	if src := PostHogKeySource(); src != KeySourceCompiled {
		t.Fatalf("key source = %q, want %q", src, KeySourceCompiled)
	}
}
