package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tickEnv(t *testing.T) *clock {
	t.Helper()
	c := env(t)
	t.Setenv(EnvTelemetryOwner, "")
	old := configOwner
	configOwner = false
	t.Cleanup(func() { configOwner = old })
	t.Setenv(EnvPostHogKey, testKey)
	grant(t, c)
	c.add(24 * time.Hour)
	return c
}

func tickOK(context.Context, []byte, time.Duration) postResult { return postResult{status: 200} }

func TestInstallTickRetryPrivacyAndRollover(t *testing.T) {
	c := tickEnv(t)
	var bodies [][]byte
	sender := func(ctx context.Context, body []byte, timeout time.Duration) postResult {
		if timeout > 2*time.Second {
			t.Error("unbounded send")
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing context deadline")
		}
		ledger, err := readInstallTick()
		if err != nil || ledger.TickID == "" || ledger.Sent {
			t.Fatalf("not reserved before send: %+v %v", ledger, err)
		}
		bodies = append(bodies, append([]byte(nil), body...))
		if len(bodies) == 1 {
			return postResult{err: errors.New("accepted, acknowledgement lost")}
		}
		return tickOK(ctx, body, timeout)
	}
	if r := maybeInstallTick(context.Background(), sender); !r.Attempted || r.Sent {
		t.Fatalf("first %+v", r)
	}
	first, _ := readInstallTick()
	if r := maybeInstallTick(context.Background(), sender); !r.Sent {
		t.Fatalf("retry %+v", r)
	}
	if len(bodies) != 2 || string(bodies[0]) != string(bodies[1]) {
		t.Fatal("retry changed event")
	}
	if r := maybeInstallTick(context.Background(), sender); r.Attempted {
		t.Fatal("resent acknowledged day")
	}
	var batch phBatch
	if err := json.Unmarshal(bodies[0], &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Batch) != 1 {
		t.Fatal("wrong batch")
	}
	event := batch.Batch[0]
	if event.Event != "install.tick" || event.UUID != first.TickID || event.DistinctID != first.TickID || event.Timestamp != first.Day+"T12:00:00Z" {
		t.Fatalf("wire %+v", event)
	}
	expected := map[string]any{"day": first.Day, "v": "9.9.9", "consent_state": "granted", "tick_id": first.TickID, "$geoip_disable": true, "$process_person_profile": false}
	a, _ := json.Marshal(event.Properties)
	b, _ := json.Marshal(expected)
	if string(a) != string(b) {
		t.Fatalf("unexpected fields: %s", a)
	}
	if strings.Contains(string(bodies[0]), LoadState().InstallID) || strings.Contains(string(bodies[0]), LoadState().Salt) {
		t.Fatal("stable identity leaked")
	}
	if batch.APIKey != redactedAPIKey {
		t.Fatal("key leak")
	}
	c.add(24 * time.Hour)
	if r := maybeInstallTick(context.Background(), sender); !r.Sent {
		t.Fatalf("next day %+v", r)
	}
	next, _ := readInstallTick()
	if next.TickID == first.TickID || next.Day == first.Day || next.LastSentDay != next.Day {
		t.Fatalf("rotation %+v", next)
	}
	c.add(-24 * time.Hour)
	if r := maybeInstallTick(context.Background(), sender); r.Attempted {
		t.Fatal("clock rollback minted another nonce")
	}
	p, _ := siblingPath(installTickFileName)
	info, err := os.Stat(p)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("ledger permissions %v %v", info, err)
	}
}

func TestInstallTickConcurrentAndLegacyWriter(t *testing.T) {
	tickEnv(t)
	var calls atomic.Int32
	sender := func(context.Context, []byte, time.Duration) postResult {
		calls.Add(1)
		time.Sleep(5 * time.Millisecond)
		return postResult{status: 200}
	}
	var wg sync.WaitGroup
	for range 64 {
		wg.Add(1)
		go func() { defer wg.Done(); maybeInstallTick(context.Background(), sender) }()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("concurrent sends %d", calls.Load())
	}
	before, _ := readInstallTick()
	// Main-state persistence remains independent of the sibling tick ledger.
	s := LoadState()
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	after, _ := readInstallTick()
	if before != after {
		t.Fatal("legacy writer changed sibling ledger")
	}
	maybeInstallTick(context.Background(), sender)
	if calls.Load() != 1 {
		t.Fatal("legacy writer allowed duplicate")
	}
}

func TestInstallTickGates(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *clock)
	}{
		{"undecided", func(t *testing.T, c *clock) { s := LoadState(); s.Consent = ConsentUndecided; _ = SaveState(s) }},
		{"declined", func(t *testing.T, c *clock) { _ = Disable("9.9.9", c.now()) }},
		{"schema", func(t *testing.T, c *clock) { s := LoadState(); s.SchemaVersion--; _ = SaveState(s) }},
		{"identity", func(t *testing.T, c *clock) { s := LoadState(); s.InstallID = ""; _ = SaveState(s) }},
		{"endpoint changed", func(t *testing.T, c *clock) { SetEndpoint("https://example.invalid") }},
		{"dnt", func(t *testing.T, c *clock) { t.Setenv(EnvDoNotTrack, "1") }},
		{"off", func(t *testing.T, c *clock) { t.Setenv(EnvTelemetry, "0") }},
		{"log", func(t *testing.T, c *clock) { t.Setenv(EnvTelemetry, "log") }},
		{"ci", func(t *testing.T, c *clock) { t.Setenv("CI", "1") }},
		{"agent", func(t *testing.T, c *clock) { t.Setenv("CLAUDECODE", "1") }},
		{"session", func(t *testing.T, c *clock) { t.Setenv("AGENTDECK_INSTANCE_ID", "a-session") }},
		{"non tty", func(t *testing.T, c *clock) { isTerminalFn = func() bool { return false } }},
		{"cli", func(t *testing.T, c *clock) { SetProcess("9.9.9", SurfaceCLI) }},
		{"dev", func(t *testing.T, c *clock) { SetProcess("dev", SurfaceTUI) }},
		{"test version", func(t *testing.T, c *clock) { SetProcess("1.16.26-test", SurfaceTUI) }},
		{"test binary", func(t *testing.T, c *clock) { testAllowed = false; t.Cleanup(func() { testAllowed = true }) }},
		{"no key", func(t *testing.T, c *clock) { t.Setenv(EnvPostHogKey, "") }},
		{"disabled config", func(t *testing.T, c *clock) { SetConfigDisabled(true) }},
		{"bad config", func(t *testing.T, c *clock) { SetConfigUnreadable() }},
		{"owner config", func(t *testing.T, c *clock) { SetConfigOwner(true) }},
		{"owner env", func(t *testing.T, c *clock) { t.Setenv(EnvTelemetryOwner, "true") }},
		{"consent day", func(t *testing.T, c *clock) { c.add(-24 * time.Hour) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := tickEnv(t)
			tc.change(t, c)
			r := maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult { t.Fatal("gate bypass"); return postResult{} })
			if r.Attempted {
				t.Fatalf("attempted: %+v", r)
			}
			p, _ := siblingPath(installTickFileName)
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Fatalf("gate created ledger: %v", err)
			}
		})
	}
}

func TestInstallTickMalformedLedgerFailsClosed(t *testing.T) {
	for _, body := range []string{`{`, `{}`, `{"day":"2026-10-04","tick_id":"bad","sent":false}`, `{"day":"invalid","tick_id":"01234567-89ab-cdef-0123-456789abcdef","sent":false}`} {
		t.Run(body, func(t *testing.T) {
			tickEnv(t)
			p, _ := siblingPath(installTickFileName)
			if err := os.WriteFile(p, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			r := maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
				t.Fatal("invalid ledger sent")
				return postResult{}
			})
			if r.Attempted || r.Reason == "" {
				t.Fatalf("not closed %+v", r)
			}
			status := ReadInstallTickStatus()
			if status.State != "unavailable" || !strings.Contains(status.Summary(), "last sent unknown") {
				t.Fatalf("corruption misreported as no prior delivery: %+v %s", status, status.Summary())
			}
			got, _ := os.ReadFile(p)
			if string(got) != body {
				t.Fatal("replaced unknown nonce")
			}
		})
	}
}

func TestInstallTickDisableResetAndPendingStatus(t *testing.T) {
	c := tickEnv(t)
	fail := func(context.Context, []byte, time.Duration) postResult {
		return postResult{err: errors.New("lost ack")}
	}
	maybeInstallTick(context.Background(), fail)
	before, _ := readInstallTick()
	if _, err := ResetID(); err != nil {
		t.Fatal(err)
	}
	after, _ := readInstallTick()
	if before != after {
		t.Fatal("reset erased tick")
	}
	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	after, _ = readInstallTick()
	if before != after {
		t.Fatal("disable erased tick")
	}
	s := LoadState()
	if err := Grant(s, "9.9.9", c.now().Add(-24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	if r := maybeInstallTick(context.Background(), tickOK); !r.Sent {
		t.Fatalf("retry after regrant %+v", r)
	}
	after, _ = readInstallTick()
	if before.TickID != after.TickID {
		t.Fatal("regrant minted nonce")
	}
}

func TestInstallTickContentionAndUnwritableLedger(t *testing.T) {
	tickEnv(t)
	unlock, err := lockState()
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	r := maybeInstallTick(context.Background(), tickOK)
	unlock()
	if time.Since(start) > 100*time.Millisecond || r.Attempted {
		t.Fatalf("lock blocked %+v", r)
	}
	p, _ := siblingPath(installTickFileName)
	if err := os.Mkdir(p, 0700); err != nil {
		t.Fatal(err)
	}
	r = maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
		t.Fatal("unreadable ledger sent")
		return postResult{}
	})
	if r.Attempted {
		t.Fatal("attempt on unreadable ledger")
	}
	if filepath.Base(p) != "telemetry-tick.json" {
		t.Fatal(p)
	}
}

func TestInstallTickProcessWorker(t *testing.T) {
	root := os.Getenv("INSTALL_TICK_TEST_HOME")
	if root == "" {
		t.Skip("subprocess only")
	}
	tickEnv(t)
	t.Setenv("HOME", root)
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	maybeInstallTick(context.Background(), func(_ context.Context, body []byte, _ time.Duration) postResult {
		f, err := os.OpenFile(filepath.Join(root, "received.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if _, err := f.Write(append(body, '\n')); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("INSTALL_TICK_TEST_FAIL") == "1" {
			return postResult{err: errors.New("accepted, lost ack before exit")}
		}
		return postResult{status: 200}
	})
}

func TestInstallTickCrossProcess(t *testing.T) {
	tickEnv(t)
	root := os.Getenv("HOME")
	var wg sync.WaitGroup
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "-test.run=^TestInstallTickProcessWorker$", "-test.count=1")
			cmd.Env = append(os.Environ(), "INSTALL_TICK_TEST_HOME="+root)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("child %v: %s", err, out)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(filepath.Join(root, "received.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Count(string(data), "\n"); lines != 1 {
		t.Fatalf("cross-process sends %d", lines)
	}
}

func TestInstallTickPreviewAndOwnerScope(t *testing.T) {
	c := tickEnv(t)
	p, _ := siblingPath(installTickFileName)
	if _, err := PreviewBatch(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("preview minted nonce")
	}
	maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
		return postResult{err: errors.New("lost ack")}
	})
	tick, _ := readInstallTick()
	bodies, err := PreviewBatch()
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 || !strings.Contains(string(bodies[0]), tick.TickID) {
		t.Fatalf("pending preview %s", bodies)
	}
	SetConfigOwner(true)
	if ok, _ := Enabled(LoadState()); !ok {
		t.Fatal("owner disabled detailed telemetry")
	}
	bodies, err = PreviewBatch()
	if err != nil || len(bodies) != 0 {
		t.Fatalf("owner preview %s %v", bodies, err)
	}
	SetConfigOwner(false)
	c.add(24 * time.Hour)
	bodies, err = PreviewBatch()
	if err != nil || len(bodies) != 0 {
		t.Fatalf("old tick preview %s %v", bodies, err)
	}
}

func TestInstallTickDisableWaitsForInflight(t *testing.T) {
	c := tickEnv(t)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
			close(entered)
			<-release
			return postResult{status: 200}
		})
	}()
	<-entered
	disabled := make(chan error, 1)
	go func() { disabled <- Disable("9.9.9", c.now()) }()
	select {
	case err := <-disabled:
		t.Errorf("disable returned before send completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-done
	if err := <-disabled; err != nil {
		t.Fatal(err)
	}
	r := maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
		t.Error("sent after disable")
		return postResult{}
	})
	if r.Attempted {
		t.Fatal("attempted after disable")
	}
}

func TestInstallTickReservationSyncFailureNeverSends(t *testing.T) {
	tickEnv(t)
	original := syncInstallTickDirectory
	t.Cleanup(func() { syncInstallTickDirectory = original })
	syncInstallTickDirectory = func(string) error { return errors.New("directory fsync failed") }
	calls := 0
	sender := func(context.Context, []byte, time.Duration) postResult { calls++; return postResult{status: 200} }
	r := maybeInstallTick(context.Background(), sender)
	if calls != 0 || r.Attempted {
		t.Fatalf("sent despite non-durable reservation: calls=%d result=%+v", calls, r)
	}
	pending, err := readInstallTick()
	if err != nil || pending.TickID == "" {
		t.Fatalf("reservation %+v %v", pending, err)
	}
	// A retry must confirm durability even if the earlier rename is visible.
	r = maybeInstallTick(context.Background(), sender)
	if calls != 0 || r.Attempted {
		t.Fatal("retry bypassed failed durability confirmation")
	}
	syncInstallTickDirectory = original
	r = maybeInstallTick(context.Background(), sender)
	after, _ := readInstallTick()
	if !r.Sent || calls != 1 || after.TickID != pending.TickID {
		t.Fatalf("retry did not reuse pending nonce: %+v %+v", r, after)
	}
}

func TestInstallTickSymlinkLedgerFailsClosed(t *testing.T) {
	tickEnv(t)
	p, _ := siblingPath(installTickFileName)
	other := filepath.Join(t.TempDir(), "ledger")
	if err := os.WriteFile(other, []byte(`{"day":"2026-10-04","tick_id":"01234567-89ab-cdef-0123-456789abcdef","v":"1.16.26","sent":false}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, p); err != nil {
		t.Fatal(err)
	}
	st := ReadInstallTickStatus()
	if st.State != "unavailable" {
		t.Fatalf("symlink ledger accepted: %+v", st)
	}
}

func TestInstallTickPreviewRespectsDestinationAndVersion(t *testing.T) {
	for _, kind := range []string{"no key", "dev"} {
		t.Run(kind, func(t *testing.T) {
			tickEnv(t)
			maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
				return postResult{err: errors.New("lost ack")}
			})
			if kind == "no key" {
				t.Setenv(EnvPostHogKey, "")
			} else {
				SetProcess("dev", SurfaceTUI)
			}
			bodies, err := PreviewBatch()
			if err != nil {
				t.Fatal(err)
			}
			for _, body := range bodies {
				if strings.Contains(string(body), `"event":"install.tick"`) {
					t.Fatalf("preview of disabled destination: %s", body)
				}
			}
		})
	}
}

func TestInstallTickRestartRetryReuse(t *testing.T) {
	tickEnv(t)
	root := os.Getenv("HOME")
	for _, fail := range []string{"1", "0"} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestInstallTickProcessWorker$", "-test.count=1")
		cmd.Env = append(os.Environ(), "INSTALL_TICK_TEST_HOME="+root, "INSTALL_TICK_TEST_FAIL="+fail)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child %v: %s", err, out)
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "received.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 || lines[0] != lines[1] {
		t.Fatalf("restart retry changed body: %s", data)
	}
}

func TestInstallTickCancellationPreservesNonce(t *testing.T) {
	tickEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	start := time.Now()
	r := maybeInstallTick(ctx, func(ctx context.Context, _ []byte, _ time.Duration) postResult {
		<-ctx.Done()
		return postResult{err: ctx.Err()}
	})
	if r.Sent || !r.Attempted || time.Since(start) > time.Second {
		t.Fatalf("deadline not respected: %+v", r)
	}
	before, _ := readInstallTick()
	if before.Sent {
		t.Fatal("cancellation acknowledged")
	}
	if r := maybeInstallTick(context.Background(), tickOK); !r.Sent {
		t.Fatalf("retry %+v", r)
	}
	after, _ := readInstallTick()
	if after.TickID != before.TickID {
		t.Fatal("cancellation replaced nonce")
	}
}

func TestInstallTickLocalCalendarMidnight(t *testing.T) {
	c := tickEnv(t)
	previous := time.Local
	time.Local = time.FixedZone("test-local", 5*3600+30*60)
	t.Cleanup(func() { time.Local = previous })
	c.set(time.Date(2026, 10, 4, 18, 29, 0, 0, time.UTC))
	if r := maybeInstallTick(context.Background(), tickOK); !r.Sent {
		t.Fatalf("first %+v", r)
	}
	before, _ := readInstallTick()
	if before.Day != "2026-10-04" {
		t.Fatal(before.Day)
	}
	c.add(2 * time.Minute)
	if r := maybeInstallTick(context.Background(), tickOK); !r.Sent {
		t.Fatalf("midnight %+v", r)
	}
	after, _ := readInstallTick()
	if after.Day != "2026-10-05" || after.TickID == before.TickID {
		t.Fatalf("local rollover %+v", after)
	}
}

func TestInstallTickRequiresFreshSchemaConsent(t *testing.T) {
	c := tickEnv(t)
	legacy := LoadState()
	legacy.SchemaVersion = 2
	if err := SaveState(legacy); err != nil {
		t.Fatal(err)
	}
	calls := 0
	sender := func(context.Context, []byte, time.Duration) postResult { calls++; return postResult{status: 200} }
	r := maybeInstallTick(context.Background(), sender)
	if r.Attempted || calls != 0 {
		t.Fatal("schema-2 grant sent new tick without updated consent")
	}
	s := LoadState()
	if ok, _ := Enabled(s); ok {
		t.Fatal("schema-2 grant still enables detailed recording")
	}
	p, _ := siblingPath(installTickFileName)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatal("old grant reserved a tick")
	}
	if s.Consent != ConsentUndecided || !ShouldPrompt(s) || s.Previous() != "v2_granted" || s.prevV1 != "" {
		t.Fatalf("wrong upgrade provenance/consent %+v previous=%s", s, s.Previous())
	}
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	if r := maybeInstallTick(context.Background(), sender); r.Attempted {
		t.Fatal("sent on renewed consent day")
	}
	c.add(24 * time.Hour)
	if r := maybeInstallTick(context.Background(), sender); !r.Sent || calls != 1 {
		t.Fatalf("renewed consent did not allow tick: %+v calls=%d", r, calls)
	}
}

func TestInstallTickSchemaUpgradeKeepsDeclinesFinal(t *testing.T) {
	tickEnv(t)
	s := LoadState()
	s.SchemaVersion = 2
	s.Consent = ConsentDeclined
	s.DeclinedSchema = 2
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	loaded := LoadState()
	if loaded.Consent != ConsentDeclined || loaded.V1Declined() || ShouldPrompt(loaded) || loaded.prevV1 != "" {
		t.Fatalf("schema-2 decline reasked or mislabeled: %+v", loaded)
	}
	if SchemaVersion != 3 {
		t.Fatalf("new event requires consent schema 3, got %d", SchemaVersion)
	}
}

func TestInstallTickV2RegrantKeepsIdentityAndLedger(t *testing.T) {
	c := tickEnv(t)
	maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
		return postResult{err: errors.New("lost ack")}
	})
	tickBefore, _ := readInstallTick()
	s := LoadState()
	s.SchemaVersion = 2
	s.Counters = map[string]int{"legacy": 1}
	s.PreV2 = false
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	spool, _ := spoolPath()
	if err := os.WriteFile(spool, []byte("legacy spool\n"), 0600); err != nil {
		t.Fatal(err)
	}
	firstDay, firstAt, oldID, oldSalt := s.FirstSeenDay, s.FirstSeenAt, s.InstallID, s.Salt
	c.add(48 * time.Hour)
	migrated := LoadState()
	if err := Grant(migrated, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(migrated); err != nil {
		t.Fatal(err)
	}
	if migrated.InstallID != oldID || migrated.Salt != oldSalt || migrated.Counters["legacy"] != 1 {
		t.Fatal("schema-only regrant changed the identity or dropped recorded data")
	}
	if migrated.FirstSeenDay != firstDay || !migrated.FirstSeenAt.Equal(firstAt) || migrated.PreV2 {
		t.Fatal("v2 regrant changed provenance")
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatalf("schema-only regrant deleted the spool: %v", err)
	}
	tickAfter, _ := readInstallTick()
	if tickAfter != tickBefore {
		t.Fatal("regrant changed daily nonce ledger")
	}
}
