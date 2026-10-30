package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/crashreport"
	"github.com/asheshgoplani/agent-deck/internal/managed"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// fakeCrashDSN is an obviously fake placeholder; example.invalid never resolves.
const fakeCrashDSN = "https://public@example.invalid/1"

func isolateManaged(t *testing.T) {
	t.Helper()
	isolateTelemetryHome(t)
	prevManaged, prevCrash := managedConfig, crashState
	t.Cleanup(func() {
		managedConfig, crashState = prevManaged, prevCrash
		telemetry.SetManaged(managed.Result{})
		telemetry.SetOrgID("", "")
	})
	t.Setenv(managed.EnvPath, "")
	t.Cleanup(func() { telemetry.SetConfigDisabled(false) })
}

func TestDecideCrashReporting(t *testing.T) {
	isolateManaged(t)
	withDSN := managed.Result{Present: true, Config: managed.Config{CrashDSN: fakeCrashDSN}}
	invalidManaged := managed.Result{Path: "/x/managed.json", Present: true, Err: errors.New("bad level")}
	cases := []struct {
		name      string
		m         managed.Result
		configDSN string
		env       map[string]string
		setup     func(t *testing.T)
		wantOn    bool
		want      string
	}{
		{"nothing configured", managed.Result{}, "", nil, nil, false, "off (no DSN configured)"},
		{"managed DSN", withDSN, "", nil, nil, true, "on (managed DSN)"},
		{"managed wins over config", withDSN, "https://other@example.invalid/2", nil, nil, true, "on (managed DSN)"},
		{"config DSN", managed.Result{}, fakeCrashDSN, nil, nil, true, "on (config DSN)"},
		{"invalid config DSN", managed.Result{}, "https://a:b@example.invalid/1", nil, nil, false, "not a valid DSN"},
		{"DO_NOT_TRACK", withDSN, "", map[string]string{telemetry.EnvDoNotTrack: "1"}, nil, false, "off (" + string(telemetry.ReasonEnvDNT) + ")"},
		{"AGENTDECK_TELEMETRY=0", withDSN, "", map[string]string{telemetry.EnvTelemetry: "0"}, nil, false, "off (" + string(telemetry.ReasonEnvTelemetry) + ")"},
		{"log mode", withDSN, "", map[string]string{telemetry.EnvTelemetry: "log"}, nil, false, "never sends"},
		{"CI", withDSN, "", map[string]string{"CI": "true"}, nil, false, "off (running in CI)"},
		{"[telemetry].disabled", withDSN, fakeCrashDSN, nil, func(*testing.T) { telemetry.SetConfigDisabled(true) },
			false, "off (" + string(telemetry.ReasonConfig) + ")"},
		{"unreadable config.toml", withDSN, "", nil, func(*testing.T) { telemetry.SetConfigUnreadable() },
			false, "off (" + string(telemetry.ReasonConfigError) + ")"},
		{"invalid managed file, valid config DSN", invalidManaged, fakeCrashDSN, nil, func(*testing.T) { telemetry.SetManaged(invalidManaged) },
			false, "off (" + string(telemetry.ReasonManagedError) + ")"},
		{"declined consent, managed DSN", withDSN, "", nil, declineForTest, false, "off (" + string(telemetry.ReasonDeclined) + ")"},
		{"declined consent, config DSN", managed.Result{}, fakeCrashDSN, nil, declineForTest, false, "off (" + string(telemetry.ReasonDeclined) + ")"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateManaged(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			if tc.setup != nil {
				tc.setup(t)
			}
			d := decideCrashReporting(tc.m, tc.configDSN)
			if d.On() != tc.wantOn || !strings.Contains(d.String(), tc.want) {
				t.Fatalf("got on=%v %q, want on=%v containing %q", d.On(), d.String(), tc.wantOn, tc.want)
			}
			if strings.Contains(d.String(), "example.invalid") {
				t.Fatalf("status line prints the DSN: %q", d.String())
			}
		})
	}
}

// declineForTest stores a declined consent in the isolated HOME, as
// `agent-deck telemetry off` does.
func declineForTest(t *testing.T) {
	t.Helper()
	if err := telemetry.Disable("9.9.9", time.Now()); err != nil {
		t.Fatal(err)
	}
}

// TestTelemetryOffTurnsCrashReportsOff: `telemetry off` (and a no at the
// `telemetry on` prompt) turns crash reports off at once and on every later
// run, and status says why.
func TestTelemetryOffTurnsCrashReportsOff(t *testing.T) {
	withDSN := managed.Result{Present: true, Config: managed.Config{CrashDSN: fakeCrashDSN}}
	for _, tc := range []struct {
		name  string
		stdin string
		args  []string
	}{
		{"telemetry off", "", []string{"off"}},
		{"no at the telemetry on prompt", "n\n", []string{"on"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateManaged(t)
			managedConfig = withDSN
			crashState = decideCrashReporting(withDSN, "")
			if !crashState.On() {
				t.Fatalf("precondition: crash reports %s, want on (undecided + managed DSN)", crashState)
			}
			if code, _, errOut := runTel(t, tc.stdin, true, tc.args...); code != 0 {
				t.Fatalf("%v exit %d: %s", tc.args, code, errOut)
			}
			want := "off (" + string(telemetry.ReasonDeclined) + ")"
			if crashState.On() || crashState.String() != want {
				t.Fatalf("after %v this process: crash reports %s, want %s", tc.args, crashState, want)
			}
			if st := statusJSON(t); st.CrashReports != want {
				t.Fatalf("status crash_reports = %q, want %q", st.CrashReports, want)
			}
			_, out, _ := runTel(t, "", true, "status")
			if !strings.Contains(out, "Crash reports: "+want) {
				t.Fatalf("status lacks the declined reason:\n%s", out)
			}
			if d := decideCrashReporting(withDSN, ""); d.On() || d.String() != want {
				t.Fatalf("next run: crash reports %s, want %s", d, want)
			}
		})
	}
}

// TestCrashReportingOffWhenManagedOff: telemetry_level = "off" in the
// managed file turns crash reports off too, even with a managed DSN.
func TestCrashReportingOffWhenManagedOff(t *testing.T) {
	isolateManaged(t)
	m := managed.Result{Present: true, Config: managed.Config{CrashDSN: fakeCrashDSN, TelemetryLevel: managed.LevelOff}}
	telemetry.SetManaged(m)
	if d := decideCrashReporting(m, ""); d.On() || d.Reason != string(telemetry.ReasonManaged) {
		t.Fatalf("crash reports %s with managed telemetry off, want off (%s)", d, telemetry.ReasonManaged)
	}
}

// TestInitCrashReportingWithoutDSNStartsNothing: the default (no DSN) path
// main() takes creates no client.
func TestInitCrashReportingWithoutDSNStartsNothing(t *testing.T) {
	isolateManaged(t)
	crashState = decideCrashReporting(managed.Result{}, "")
	initCrashReporting()
	if crashreport.Enabled() {
		t.Fatal("crash reporting started without a DSN")
	}
}

// TestTelemetryStatusShowsOrgManagedAndCrash: status shows the org id (or
// none), the managed file and whether crash reports are on, never the DSN.
func TestTelemetryStatusShowsOrgManagedAndCrash(t *testing.T) {
	isolateManaged(t)
	managedConfig, crashState = managed.Result{}, decideCrashReporting(managed.Result{}, "")
	st := statusJSON(t)
	if st.OrgID != "none" || st.OrgSource != telemetry.OrgSourceNone || st.Managed != "none" || st.CrashReports != "off (no DSN configured)" {
		t.Fatalf("default status: org %q/%q managed %q crash %q", st.OrgID, st.OrgSource, st.Managed, st.CrashReports)
	}

	p := filepath.Join(t.TempDir(), "managed.json")
	body := `{"org_id":"pilot-7f3k2","telemetry_level":"basic","crash_dsn":"` + fakeCrashDSN + `","update_channel":"pilot"}`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	managedConfig = managed.LoadFile(p)
	telemetry.SetManaged(managedConfig)
	telemetry.SetOrgID(managedConfig.OrgID, "")
	crashState = decideCrashReporting(managedConfig, "")

	code, out, _ := runTel(t, "", true, "status")
	if code != 0 {
		t.Fatalf("status exit %d", code)
	}
	for _, want := range []string{
		"Level:         basic",
		"Org id:        pilot-7f3k2 (managed)",
		"Managed:       " + p + " (level ceiling basic, update channel pilot)",
		"Crash reports: on (managed DSN)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "example.invalid") {
		t.Fatalf("status prints the DSN:\n%s", out)
	}

	managedConfig = managed.Result{Path: p, Present: true, Err: errors.New("bad")}
	if got := managedStatusLine(managedConfig); !strings.Contains(got, "unreadable") {
		t.Fatalf("unreadable managed line %q", got)
	}
}
