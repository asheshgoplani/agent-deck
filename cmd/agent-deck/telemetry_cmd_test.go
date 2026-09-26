package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

func isolateTelemetryHome(t *testing.T) {
	t.Helper()
	telemetry.EnableForTest(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", home+"/data")
	t.Setenv("XDG_CONFIG_HOME", home+"/config")
	t.Setenv("XDG_CACHE_HOME", home+"/cache")
	for _, k := range []string{telemetry.EnvTelemetry, telemetry.EnvDoNotTrack, telemetry.EnvPostHogKey, "AGENTDECK_INSTANCE_ID", "AGENT_DECK_SESSION_ID",
		"CLAUDECODE", "GEMINI_CLI", "CURSOR_AGENT", "CODEX_SANDBOX", "CODEX_THREAD_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	telemetry.ClearCIForTest(t)
	telemetry.SetConfigDisabled(false)
	telemetry.SetEndpoint("")
}

func runTel(t *testing.T, stdin string, interactive bool, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runTelemetry(args, "9.9.9", strings.NewReader(stdin), &o, &e, interactive)
	return code, o.String(), e.String()
}

func statusJSON(t *testing.T) telemetryStatus {
	t.Helper()
	code, out, _ := runTel(t, "", true, "status", "--json")
	if code != 0 {
		t.Fatalf("status exit %d", code)
	}
	var st telemetryStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil {
		t.Fatalf("status json: %v\n%s", err, out)
	}
	return st
}

func TestTelemetryStatusDefaultOffNotConfigured(t *testing.T) {
	isolateTelemetryHome(t)
	st := statusJSON(t)
	if st.Enabled || st.Consent != "undecided" || st.InstallID != "" || st.SchemaVersion != 2 {
		t.Fatalf("default status %+v", st)
	}
	if !strings.HasPrefix(st.Upload, "not configured") || st.Endpoint != telemetry.DefaultEndpoint || st.Level != "full" {
		t.Fatalf("upload %q endpoint %q level %q", st.Upload, st.Endpoint, st.Level)
	}
	_, out, _ := runTel(t, "", true, "status")
	for _, want := range []string{"Telemetry: OFF", "Upload:        not configured", "Spool:         0 event(s)", "Daily cap:     0/60"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestTelemetryOnRequiresExplicitY(t *testing.T) {
	for _, answer := range []string{"\n", "yes please\n", "n\n", ""} {
		isolateTelemetryHome(t)
		code, out, _ := runTel(t, answer, true, "on")
		if code != 0 || statusJSON(t).Enabled {
			t.Fatalf("answer %q enabled telemetry", answer)
		}
		if !strings.Contains(out, "Help improve agent-deck?") || !strings.Contains(out, "[y/N]") {
			t.Fatalf("disclosure not shown:\n%s", out)
		}
	}
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "y\n", true, "on")
	st := statusJSON(t)
	if code != 0 || !st.Enabled || len(st.InstallID) != 32 {
		t.Fatalf("y did not enable: %s %+v", out, st)
	}
	if !strings.Contains(out, "Nothing is sent before tomorrow") || !strings.Contains(out, "No upload destination is configured") {
		t.Fatalf("confirmation:\n%s", out)
	}
}

func TestTelemetryOnRefusals(t *testing.T) {
	cases := []struct {
		name        string
		env         map[string]string
		args        []string
		interactive bool
	}{
		{"not_a_terminal", nil, []string{"on"}, false},
		{"yes_flag", nil, []string{"on", "--yes"}, true},
		{"inside_session", map[string]string{"AGENTDECK_INSTANCE_ID": "x"}, []string{"enable"}, true},
		{"ci", map[string]string{"CI": "1"}, []string{"on"}, true},
		{"claude_code", map[string]string{"CLAUDECODE": "1"}, []string{"on"}, true},
		{"gemini_cli", map[string]string{"GEMINI_CLI": "1"}, []string{"on"}, true},
		{"cursor_agent", map[string]string{"CURSOR_AGENT": "1"}, []string{"on"}, true},
		{"codex", map[string]string{"CODEX_SANDBOX": "seatbelt"}, []string{"on"}, true},
		{"dnt", map[string]string{telemetry.EnvDoNotTrack: "1"}, []string{"on"}, true},
		{"log_mode", map[string]string{telemetry.EnvTelemetry: "log"}, []string{"on"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateTelemetryHome(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			code, _, errOut := runTel(t, "y\n", tc.interactive, tc.args...)
			if code == 0 || errOut == "" {
				t.Fatalf("exit %d, stderr %q", code, errOut)
			}
			if telemetry.LoadState().Consent == telemetry.ConsentGranted {
				t.Fatal("consent granted")
			}
		})
	}
}

func TestTelemetryJSONModeWritesQuestionToStderr(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, errOut := runTel(t, "y\n", true, "on", "--json")
	if code != 0 || !strings.Contains(errOut, "Help improve agent-deck?") {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	var st telemetryStatus
	if err := json.Unmarshal([]byte(out), &st); err != nil || !st.Enabled {
		t.Fatalf("stdout %q", out)
	}
}

type failedDisclosure struct{}

func (failedDisclosure) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTelemetryFailedDisclosureCannotGrant(t *testing.T) {
	isolateTelemetryHome(t)
	code := runTelemetry([]string{"on"}, "9.9.9", strings.NewReader("y\n"), failedDisclosure{}, io.Discard, true)
	if code == 0 || telemetry.LoadState().Consent == telemetry.ConsentGranted {
		t.Fatal("consent without a visible disclosure")
	}
}

func TestTelemetryOffDeletesIdentityAndSpool(t *testing.T) {
	isolateTelemetryHome(t)
	runTel(t, "y\n", true, "on")
	code, out, _ := runTel(t, "", true, "off")
	st := statusJSON(t)
	if code != 0 || st.Enabled || st.InstallID != "" || st.Consent != "declined" || st.Spool.Events != 0 {
		t.Fatalf("off: %s %+v", out, st)
	}
	if code, _, _ := runTel(t, "", true, "reset-id"); code == 0 {
		t.Fatal("reset-id without consent must fail")
	}
}

func TestTelemetryResetIDKeepsConsent(t *testing.T) {
	isolateTelemetryHome(t)
	runTel(t, "y\n", true, "on")
	before := statusJSON(t).InstallID
	code, out, _ := runTel(t, "", true, "reset-id")
	after := statusJSON(t)
	if code != 0 || after.InstallID == before || !after.Enabled {
		t.Fatalf("reset-id: %s", out)
	}
}

func TestTelemetryPreviewNeverCreatesAnID(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "", true, "preview")
	if code != 0 || !strings.Contains(out, "Nothing is recorded or sent while telemetry is off") {
		t.Fatalf("preview: %s", out)
	}
	if telemetry.LoadState().InstallID != "" {
		t.Fatal("preview created an id")
	}
	_, out, _ = runTel(t, "", true, "preview", "--json")
	if !strings.Contains(out, `"requests": []`) {
		t.Fatalf("preview json: %s", out)
	}
}

func TestTelemetryShowLastBeforeAnySend(t *testing.T) {
	isolateTelemetryHome(t)
	_, out, _ := runTel(t, "", true, "show-last", "--json")
	if !strings.Contains(out, `"sent": false`) {
		t.Fatalf("show-last: %s", out)
	}
}

func TestTelemetryLevel(t *testing.T) {
	isolateTelemetryHome(t)
	runTel(t, "y\n", true, "on")
	if code, _, _ := runTel(t, "", true, "level", "basic"); code != 0 || statusJSON(t).Level != "basic" {
		t.Fatal("lowering to basic")
	}
	if code, out, _ := runTel(t, "\n", true, "level", "full"); code != 0 || statusJSON(t).Level != "basic" {
		t.Fatalf("raising without confirmation: %s", out)
	}
	if code, _, _ := runTel(t, "", false, "level", "full"); code == 0 {
		t.Fatal("raising from a non-terminal")
	}
	if code, _, _ := runTel(t, "y\n", true, "level", "full"); code != 0 || statusJSON(t).Level != "full" {
		t.Fatal("raising with confirmation")
	}
	if code, _, _ := runTel(t, "", true, "level", "max"); code != 2 {
		t.Fatal("invalid level accepted")
	}
}

// TestTelemetrySchemaMarkdownMatchesTELEMETRYmd: the published field list in
// TELEMETRY.md is generated by `telemetry schema --markdown` and cannot drift.
func TestTelemetrySchemaMarkdownMatchesTELEMETRYmd(t *testing.T) {
	isolateTelemetryHome(t)
	code, out, _ := runTel(t, "", true, "schema", "--markdown")
	if code != 0 {
		t.Fatal(code)
	}
	doc, err := os.ReadFile("../../TELEMETRY.md")
	if err != nil {
		t.Fatal(err)
	}
	const begin, end = "<!-- schema:begin (generated by `agent-deck telemetry schema --markdown`; do not edit) -->\n", "<!-- schema:end -->"
	s := string(doc)
	i, j := strings.Index(s, begin), strings.Index(s, end)
	if i < 0 || j < i {
		t.Fatal("TELEMETRY.md lacks the schema markers")
	}
	if got := s[i+len(begin) : j]; got != out {
		t.Fatalf("TELEMETRY.md schema table is stale; regenerate with `agent-deck telemetry schema --markdown`.\n--- generated\n%s", out)
	}
	for _, line := range strings.Split(telemetry.PromptText(telemetry.DefaultEndpoint), "\n") {
		if !strings.Contains(s, line) {
			t.Fatalf("TELEMETRY.md does not reproduce the consent line %q", line)
		}
	}
	code, out, _ = runTel(t, "", true, "schema", "--json")
	var parsed struct {
		Schema int `json:"schema"`
		Events []struct {
			Name string `json:"name"`
		} `json:"events"`
	}
	if code != 0 || json.Unmarshal([]byte(out), &parsed) != nil || parsed.Schema != 2 || len(parsed.Events) != 29 {
		t.Fatalf("schema --json: %d %.200s", code, out)
	}
}

func TestCLIFeatureTableUsesOnlyPublishedFeatures(t *testing.T) {
	for cmd, entry := range cliFeatures {
		all := []telemetry.Feature{entry.feature}
		for _, f := range entry.sub {
			all = append(all, f)
		}
		for _, f := range all {
			if f == "" {
				continue
			}
			found := false
			for _, v := range telemetry.FeatureValues {
				if v == string(f) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s maps to unpublished feature %q", cmd, f)
			}
		}
	}
	for _, excluded := range []string{"hook-handler", "codex-notify", "notify-daemon", "daemon", "run-task", "__complete", "completion", "telemetry"} {
		if _, ok := cliFeatureFor(excluded, nil); ok {
			t.Errorf("%s must not be counted", excluded)
		}
	}
	if f, ok := cliFeatureFor("session", []string{"send", "x"}); !ok || f != "session_send" {
		t.Fatal("session send")
	}
	if _, ok := cliFeatureFor("list", []string{"--help"}); ok {
		t.Fatal("--help counted")
	}
}

func TestTelemetryHelpAndUnknown(t *testing.T) {
	isolateTelemetryHome(t)
	_, out, _ := runTel(t, "", true, "--help")
	for _, want := range []string{"on | enable", "off | disable", "schema", "level full|basic", "AGENTDECK_TELEMETRY=log", "DO_NOT_TRACK=1", "posthog_key"} {
		if !strings.Contains(out, want) {
			t.Errorf("help lacks %q", want)
		}
	}
	if code, _, _ := runTel(t, "", true, "bogus"); code != 2 {
		t.Fatal("unknown subcommand")
	}
	if code, _, _ := runTel(t, "", true, "status", "--bogus"); code != 2 {
		t.Fatal("unknown flag")
	}
	if code, _, _ := runTel(t, "", true, "status", "extra"); code != 2 {
		t.Fatal("extra argument")
	}
}
