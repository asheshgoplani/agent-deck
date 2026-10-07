package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// The CLI used to count a command's feature (and its first-use funnel
// milestone) before the command ran, so failed and skipped commands counted
// as successful uses. These tests run the real dispatch in main() in a
// subprocess (handlers call os.Exit) against an isolated HOME with consent
// granted, then read the local telemetry state and spool.

const cliTelHelperEnv = "AGENT_DECK_CLI_TEL_HELPER"

// TestCLITelemetryOutcomeHelper is a subprocess entrypoint, not a test.
func TestCLITelemetryOutcomeHelper(t *testing.T) {
	if os.Getenv(cliTelHelperEnv) != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) > 0 && args[0] == "__seed_fresh_session" {
		// A healthy session started just now: `session restart` must skip it.
		storage, err := session.NewStorageWithProfile("")
		if err != nil {
			t.Fatal(err)
		}
		inst := session.NewInstanceWithTool(args[1], t.TempDir(), "shell")
		inst.Status = session.StatusIdle
		inst.LastStartedAt = time.Now()
		if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	}
	telemetry.EnableForTest(t)
	telemetry.SetTerminalForTest(t, true)
	os.Args = append([]string{"agent-deck"}, args...)
	main()
	os.Exit(0)
}

func runCLITelHelper(t *testing.T, args ...string) int {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLITelemetryOutcomeHelper$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), cliTelHelperEnv+"=1", "AGENT_DECK_TASK6_HELPER_PROCESS=1")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("helper %v: %v\n%s", args, err, out.String())
		}
		code = ee.ExitCode()
	}
	t.Logf("agent-deck %v -> rc=%d\n%s", args, code, out.String())
	return code
}

// cliTelHome isolates HOME and grants consent (the parent then reads the same
// state the helper subprocesses write).
func cliTelHome(t *testing.T) {
	t.Helper()
	isolateTelemetryHome(t)
	if code, out, _ := runTel(t, "y\n", true, "on"); code != 0 {
		t.Fatalf("telemetry on: %d\n%s", code, out)
	}
}

type cliTelTotals struct {
	cliCmds  int
	features map[string]telemetry.FeatureCount
	steps    map[string]bool
}

func readCLITel(t *testing.T) cliTelTotals {
	t.Helper()
	tot := cliTelTotals{features: map[string]telemetry.FeatureCount{}, steps: map[string]bool{}}
	for _, r := range telemetry.LoadState().Daily {
		tot.cliCmds += r.CLICmds
		for k, c := range r.Features {
			fc := tot.features[k]
			fc.Count += c.Count
			fc.Errors += c.Errors
			tot.features[k] = fc
		}
	}
	sp, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(filepath.Dir(sp), telemetry.SpoolFileName))
	if err != nil {
		if os.IsNotExist(err) {
			return tot
		}
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var l struct {
			E string         `json:"e"`
			P map[string]any `json:"p"`
		}
		if json.Unmarshal(sc.Bytes(), &l) == nil && l.E == "onboard.milestone" {
			if s, ok := l.P["step"].(string); ok {
				tot.steps[s] = true
			}
		}
	}
	return tot
}

// A failing `mcp attach` counts one mcp_attach use with an error and does not
// reach first_mcp_attach; the invocation still counts in cli_cmds.
func TestCLITelemetry_FailedCommandCountsErrorNoMilestone(t *testing.T) {
	cliTelHome(t)
	if rc := runCLITelHelper(t, "mcp", "attach", "nosuch", "github"); rc == 0 {
		t.Fatal("mcp attach on a missing session succeeded; the fixture proves nothing")
	}
	got := readCLITel(t)
	if fc := got.features["mcp_attach"]; fc.Count != 1 || fc.Errors != 1 {
		t.Errorf("mcp_attach = %+v, want count 1 errors 1", fc)
	}
	if got.steps["first_mcp_attach"] {
		t.Error("failed mcp attach reached the first_mcp_attach milestone")
	}
	if got.cliCmds != 1 {
		t.Errorf("cli_cmds = %d, want 1", got.cliCmds)
	}
}

// `fleet status` is a read-only report, not a fleet launch.
func TestCLITelemetry_FleetStatusIsNotFleetLaunch(t *testing.T) {
	cliTelHome(t)
	if rc := runCLITelHelper(t, "fleet", "status"); rc != 0 {
		t.Fatalf("fleet status rc=%d", rc)
	}
	got := readCLITel(t)
	if _, ok := got.features["fleet_launch"]; ok {
		t.Errorf("fleet status counted as fleet_launch: %+v", got.features)
	}
	if got.steps["first_fleet"] {
		t.Error("fleet status reached the first_fleet milestone")
	}
	if fc := got.features["fleet_status"]; fc.Count != 1 || fc.Errors != 0 {
		t.Errorf("fleet_status = %+v, want count 1 errors 0", fc)
	}
}

// A restart skipped by the freshness guard did nothing: it is an invocation
// (cli_cmds) but not a restart.
func TestCLITelemetry_SkippedRestartNotCounted(t *testing.T) {
	cliTelHome(t)
	if rc := runCLITelHelper(t, "__seed_fresh_session", "fresh"); rc != 0 {
		t.Fatalf("seed rc=%d", rc)
	}
	if rc := runCLITelHelper(t, "session", "restart", "fresh"); rc != 0 {
		t.Fatalf("session restart rc=%d", rc)
	}
	got := readCLITel(t)
	if fc, ok := got.features["restart"]; ok {
		t.Errorf("skipped restart counted as restart: %+v", fc)
	}
	if got.cliCmds != 1 {
		t.Errorf("cli_cmds = %d, want 1", got.cliCmds)
	}
}
