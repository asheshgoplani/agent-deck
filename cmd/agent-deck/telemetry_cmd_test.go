package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

func isolateTelemetryHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack, "CI", "GITHUB_ACTIONS", "AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	telemetry.SetConfigDisabled(false)
}

func runTel(t *testing.T, stdin string, interactive bool, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runTelemetry(args, "9.9.9", strings.NewReader(stdin), &o, &e, interactive)
	return code, o.String(), e.String()
}

func TestTelemetryStatusDefaultOffJSON(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "", true, "status", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var st telemetryStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	if st.Enabled || st.Consent != "undecided" || st.InstallID != "" {
		t.Fatalf("fresh status = %+v", st)
	}
	if st.Reason == "" || st.Endpoint == "" || st.StatePath == "" {
		t.Fatalf("status missing fields: %+v", st)
	}
	if code, out2, _ := runTel(t, "", true, "--json"); code != 0 || out2 != out {
		t.Fatalf("bare telemetry differs from status: %d %s", code, out2)
	}
}

func TestTelemetryStatusHumanReadable(t *testing.T) {
	isolateTelemetryHome(t)
	_, out, _ := runTel(t, "", true, "status")
	for _, want := range []string{"Telemetry: OFF", "consent has not been given", "Endpoint:", "Last sent:     never", telemetry.DocsURL} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestTelemetryEnableRefusesOnNonTerminalWithoutYes(t *testing.T) {
	isolateTelemetryHome(t)
	code, _, errOut := runTel(t, "y\n", false, "enable")
	if code == 0 {
		t.Fatal("enable succeeded on a non-terminal")
	}
	if !strings.Contains(errOut, "not a terminal") {
		t.Fatalf("stderr = %q", errOut)
	}
	if telemetry.LoadState().Consent != telemetry.ConsentUndecided {
		t.Fatal("state changed")
	}
}

func TestTelemetryEnableInteractiveDefaultIsNo(t *testing.T) {
	isolateTelemetryHome(t)
	for _, answer := range []string{"\n", "n\n", "N\n", "maybe\n", ""} {
		isolateTelemetryHome(t)
		code, out, _ := runTel(t, answer, true, "enable")
		if code != 0 {
			t.Fatalf("answer %q: exit %d", answer, code)
		}
		if !strings.Contains(out, telemetry.DocsURL) || !strings.Contains(out, "[y/N]") {
			t.Fatalf("disclosure not shown for %q:\n%s", answer, out)
		}
		st := telemetry.LoadState()
		if st.Consent != telemetry.ConsentDeclined || st.InstallID != "" {
			t.Fatalf("answer %q: state = %+v", answer, st)
		}
	}
}

func TestTelemetryEnableInteractiveYes(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "y\n", true, "enable")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if !strings.Contains(out, "Help improve agent-deck?") || !strings.Contains(out, telemetry.Endpoint()) {
		t.Fatalf("disclosure missing:\n%s", out)
	}
	st := telemetry.LoadState()
	if st.Consent != telemetry.ConsentGranted || len(st.InstallID) != 32 || st.ConsentVersion != "9.9.9" {
		t.Fatalf("state = %+v", st)
	}
}

func TestTelemetryEnableJSONRequiresInteractiveAnswer(t *testing.T) {
	isolateTelemetryHome(t)
	if code, _, _ := runTel(t, "", false, "enable", "--yes", "--json"); code == 0 {
		t.Fatal("script consent accepted")
	}
	code, out, disclosure := runTel(t, "y\n", true, "enable", "--json")
	var st telemetryStatus
	if code != 0 || json.Unmarshal([]byte(out), &st) != nil || !st.Enabled {
		t.Fatalf("code=%d out=%s", code, out)
	}
	if !strings.Contains(disclosure, telemetry.DocsURL) {
		t.Fatal("missing disclosure on stderr")
	}
}

func TestTelemetryEOFDoesNotGrant(t *testing.T) {
	isolateTelemetryHome(t)
	runTel(t, "y", true, "enable")
	if telemetry.LoadState().Consent == telemetry.ConsentGranted {
		t.Fatal("EOF granted consent")
	}
}

func TestTelemetryEnableYesRefusedInsideSessionOrCI(t *testing.T) {
	for _, k := range []string{"AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID", "CI", "GITHUB_ACTIONS"} {
		t.Run(k, func(t *testing.T) {
			isolateTelemetryHome(t)
			t.Setenv(k, "1")
			code, _, errOut := runTel(t, "y\n", true, "enable", "--yes")
			if code == 0 || !strings.Contains(errOut, "must be given by a person") {
				t.Fatalf("--yes under %s: code=%d stderr=%q", k, code, errOut)
			}
			if telemetry.LoadState().Consent != telemetry.ConsentUndecided {
				t.Fatal("consent recorded by an agent/CI caller")
			}
			code, _, _ = runTel(t, "y\n", false, "enable")
			if code == 0 || telemetry.LoadState().Consent != telemetry.ConsentUndecided {
				t.Fatal("non-interactive enable changed state")
			}
		})
	}
}

func TestTelemetryEnableRefusedUnderKillSwitch(t *testing.T) {
	isolateTelemetryHome(t)
	t.Setenv(telemetry.EnvDoNotTrack, "1")
	code, _, errOut := runTel(t, "y\n", true, "enable", "--yes")
	if code == 0 || !strings.Contains(errOut, "DO_NOT_TRACK") {
		t.Fatalf("enable under DNT: code=%d stderr=%q", code, errOut)
	}
	if telemetry.LoadState().Consent != telemetry.ConsentUndecided {
		t.Fatal("consent recorded despite kill switch")
	}
}

func TestTelemetryDisableAndResetID(t *testing.T) {
	isolateTelemetryHome(t)
	runTel(t, "y\n", true, "enable")
	first := telemetry.LoadState().InstallID

	code, out, _ := runTel(t, "", false, "reset-id", "--json")
	if code != 0 {
		t.Fatalf("reset-id exit %d", code)
	}
	var st telemetryStatus
	json.Unmarshal([]byte(out), &st)
	if st.InstallID == first || len(st.InstallID) != 32 {
		t.Fatalf("reset-id: %q -> %q", first, st.InstallID)
	}

	code, out, _ = runTel(t, "", false, "disable", "--json")
	if code != 0 {
		t.Fatalf("disable exit %d", code)
	}
	st = telemetryStatus{}
	json.Unmarshal([]byte(out), &st)
	if st.Enabled || st.Consent != "declined" || st.InstallID != "" {
		t.Fatalf("after disable: %+v", st)
	}
	if code, _, errOut := runTel(t, "", false, "reset-id"); code == 0 || !strings.Contains(errOut, "not enabled") {
		t.Fatalf("reset-id while disabled: %d %q", code, errOut)
	}
}

func TestTelemetryShowLast(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "", false, "show-last", "--json")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil || v["sent"] != false {
		t.Fatalf("show-last before any send: %v %s", err, out)
	}
	_, out, _ = runTel(t, "", false, "show-last")
	if !strings.Contains(out, "Nothing has ever been sent") {
		t.Fatalf("human show-last: %s", out)
	}

	s := telemetry.LoadState()
	s.LastSentDay = "2026-09-04"
	s.LastPayload = json.RawMessage(`{"schema_version":1,"install_id":"ab","version":"9.9.9","os":"linux","arch":"arm64","day":"2026-09-04","counters":{"tui_launches":1}}`)
	telemetry.SaveState(s)
	code, out, _ = runTel(t, "", false, "show-last", "--json")
	if err := json.Unmarshal([]byte(out), &v); err != nil || code != 0 || v["sent"] != true {
		t.Fatalf("show-last after send: %v %s", err, out)
	}
	payload := v["payload"].(map[string]any)
	if payload["day"] != "2026-09-04" || payload["counters"].(map[string]any)["tui_launches"] != 1.0 {
		t.Fatalf("payload = %v", payload)
	}
	_, out, _ = runTel(t, "", false, "show-last")
	if !strings.Contains(out, "Last report sent on 2026-09-04") || !strings.Contains(out, `"tui_launches": 1`) {
		t.Fatalf("human show-last after send: %s", out)
	}
}

func TestTelemetryHelpDocumentsEverything(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "", false, "--help")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	want := []string{"status", "enable", "disable", "show-last", "reset-id", "--json", "--yes",
		"AGENTDECK_TELEMETRY=0", "DO_NOT_TRACK=1", "[telemetry] endpoint", "OFF by default",
		telemetry.DocsURL, "install_id", "schema_version", "counters", "At most one", "Never sent"}
	want = append(want, telemetry.AllowedCounterKeys()...)
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("help lacks %q", w)
		}
	}
	if code, _, _ := runTel(t, "", false, "help"); code != 0 {
		t.Fatal("`telemetry help` failed")
	}
}

func TestTelemetryUnknownSubcommandAndFlag(t *testing.T) {
	isolateTelemetryHome(t)
	if code, _, errOut := runTel(t, "", false, "bogus"); code != 2 || !strings.Contains(errOut, "unknown subcommand") {
		t.Fatalf("bogus: %d %q", code, errOut)
	}
	if code, _, errOut := runTel(t, "", false, "status", "--nope"); code != 2 || !strings.Contains(errOut, "unknown flag") {
		t.Fatalf("flag: %d %q", code, errOut)
	}
}
