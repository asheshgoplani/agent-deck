package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
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

// A mistyped flag used to exit inside the flag package (flag.ExitOnError
// calls os.Exit itself), skipping exitCLI: the invocation was not counted at
// all. It is a failed use of the command's feature, exit code 2 as before.
func TestCLITelemetry_FlagParseErrorCountedAsFailure(t *testing.T) {
	cliTelHome(t)
	if rc := runCLITelHelper(t, "fleet", "recover", "--bogus"); rc != 2 {
		t.Fatalf("fleet recover --bogus rc=%d, want 2", rc)
	}
	got := readCLITel(t)
	if got.cliCmds != 1 {
		t.Errorf("cli_cmds = %d, want 1", got.cliCmds)
	}
	if fc := got.features["fleet_recover"]; fc.Count != 1 || fc.Errors != 1 {
		t.Errorf("fleet_recover = %+v, want count 1 errors 1", fc)
	}
}

// `-help` (which cliFeatureFor does not filter) prints usage and exits 0: an
// invocation, not a use of the feature.
func TestCLITelemetry_FlagHelpCountsInvocationOnly(t *testing.T) {
	cliTelHome(t)
	if rc := runCLITelHelper(t, "fleet", "recover", "-help"); rc != 0 {
		t.Fatalf("fleet recover -help rc=%d, want 0", rc)
	}
	got := readCLITel(t)
	if got.cliCmds != 1 {
		t.Errorf("cli_cmds = %d, want 1", got.cliCmds)
	}
	if fc, ok := got.features["fleet_recover"]; ok {
		t.Errorf("-help counted as fleet_recover: %+v", fc)
	}
}

// No CLI FlagSet may exit on its own: flag.ExitOnError bypasses exitCLI.
// Use flag.ContinueOnError with parseCLIFlags.
var exitOnErrorFlagSet = regexp.MustCompile(`NewFlagSet\(.*flag\.ExitOnError\)`)

func TestCLIFlagSetsDoNotExitOnTheirOwn(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if exitOnErrorFlagSet.MatchString(line) {
				t.Errorf("%s:%d uses flag.ExitOnError, which exits without recording telemetry", f, i+1)
			}
		}
	}
}

// Uninstall removes the telemetry data; recording the command after that
// recreated the data dir with a lock file. A clean uninstall leaves nothing.
func TestCLITelemetry_UninstallLeavesNoTelemetryDir(t *testing.T) {
	cliTelHome(t)
	sp, err := telemetry.StatePath()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(sp)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture: telemetry dir missing after consent: %v", err)
	}
	t.Setenv("PATH", t.TempDir()) // no brew: the uninstaller only sees this HOME
	if rc := runCLITelHelper(t, "uninstall", "-y", "--keep-tmux-config"); rc != 0 {
		t.Fatalf("uninstall rc=%d", rc)
	}
	if entries, err := os.ReadDir(dir); !os.IsNotExist(err) {
		names := []string{}
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("telemetry dir %s left behind after uninstall (err=%v, entries=%v)", dir, err, names)
	}
}

// startCLITelHelper starts the helper without waiting, for long-running
// commands such as `web`.
func startCLITelHelper(t *testing.T, args ...string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestCLITelemetryOutcomeHelper$", "--"}, args...)...)
	cmd.Env = append(os.Environ(), cliTelHelperEnv+"=1", "AGENT_DECK_TASK6_HELPER_PROCESS=1")
	out := &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, out
}

func waitCLITelHelper(t *testing.T, cmd *exec.Cmd, out *bytes.Buffer, limit time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		t.Logf("helper rc=%d\n%s", code, out.String())
		return code
	case <-time.After(limit):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("helper still running after %s\n%s", limit, out.String())
		return -1
	}
}

// `web` whose server cannot bind (port in use) failed: it is not a
// successful web_ui use.
func TestCLITelemetry_WebBindFailureCountsError(t *testing.T) {
	cliTelHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cmd, out := startCLITelHelper(t, "web", "--no-tui", "--listen", ln.Addr().String())
	if rc := waitCLITelHelper(t, cmd, out, 60*time.Second); rc == 0 {
		t.Fatal("web on a busy port exited 0; the fixture proves nothing")
	}
	got := readCLITel(t)
	if fc := got.features["web_ui"]; fc.Count != 1 || fc.Errors != 1 {
		t.Errorf("web_ui = %+v, want count 1 errors 1", fc)
	}
	if got.cliCmds != 1 {
		t.Errorf("cli_cmds = %d, want 1", got.cliCmds)
	}
}

// A web server that is listening counts as one successful web_ui use while it
// runs, not only when it exits.
func TestCLITelemetry_WebCountedOnceListening(t *testing.T) {
	cliTelHome(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	cmd, out := startCLITelHelper(t, "web", "--no-tui", "--listen", addr)
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Logf("helper output:\n%s", out.String())
	}()
	deadline := time.Now().Add(60 * time.Second)
	for {
		if fc := readCLITel(t).features["web_ui"]; fc.Count > 0 {
			if fc.Count != 1 || fc.Errors != 0 {
				t.Errorf("web_ui = %+v, want count 1 errors 0", fc)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listening web server never counted web_ui")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if c, err := net.DialTimeout("tcp", addr, 5*time.Second); err != nil {
		t.Errorf("web_ui counted but server not listening: %v", err)
	} else {
		c.Close()
	}
}
