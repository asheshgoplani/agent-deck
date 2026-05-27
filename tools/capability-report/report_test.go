package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixedTime is a stable timestamp so dashboard rendering is deterministic.
var fixedTime = time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)

// sampleResults pretends the Wave 1 fast-gate tests all passed, except one we
// flip to fail in dedicated cases.
func sampleResults() map[string]testResult {
	return map[string]testResult{
		"TestCapability_Lifecycle_Add":       {status: StatusPass, elapsed: 2.0},
		"TestCapability_Lifecycle_Start":     {status: StatusPass, elapsed: 1.4},
		"TestCapability_Lifecycle_Stop":      {status: StatusPass, elapsed: 1.4},
		"TestCapability_Lifecycle_Restart":   {status: StatusPass, elapsed: 2.5},
		"TestCapability_Lifecycle_Rm":        {status: StatusPass, elapsed: 11.0},
		"TestCapability_Lifecycle_Launch":    {status: StatusPass, elapsed: 7.1},
		"TestCapability_Lifecycle_Fork":      {status: StatusPass, elapsed: 0.1},
		"TestCapability_Agent_EchoRoundTrip": {status: StatusPass, elapsed: 5.7},
		// Wave 2 fast-gate capabilities.
		"TestCapability_MCP_AttachDetach":         {status: StatusPass, elapsed: 2.8},
		"TestCapability_Worktree_CreateFinish":    {status: StatusPass, elapsed: 5.5},
		"TestCapability_Groups_Filtering":         {status: StatusPass, elapsed: 1.0},
		"TestCapability_Profiles_Isolation":       {status: StatusPass, elapsed: 1.1},
		"TestCapability_MultiTool_Readiness":      {status: StatusPass, elapsed: 15.8},
		"TestCapability_Conductor_FinishedSignal": {status: StatusPass, elapsed: 3.0},
		"TestCapability_Conductor_Dedup":          {status: StatusPass, elapsed: 0.4},
	}
}

func TestParseTestResults(t *testing.T) {
	jsonl := `
{"Time":"2026-05-26T12:00:00Z","Action":"run","Test":"TestCapability_Lifecycle_Add"}
{"Time":"2026-05-26T12:00:02Z","Action":"pass","Test":"TestCapability_Lifecycle_Add","Elapsed":2.0}
{"Time":"2026-05-26T12:00:03Z","Action":"fail","Test":"TestCapability_Lifecycle_Stop","Elapsed":1.5}
not-json build noise
{"Action":"pass","Elapsed":0.1}
`
	got := ParseTestResults([]byte(jsonl))
	if r, ok := got["TestCapability_Lifecycle_Add"]; !ok || r.status != StatusPass || r.elapsed != 2.0 {
		t.Errorf("Add result = %+v, want pass/2.0", r)
	}
	if r, ok := got["TestCapability_Lifecycle_Stop"]; !ok || r.status != StatusFail {
		t.Errorf("Stop result = %+v, want fail", r)
	}
	// The package-level event with no Test field must be ignored.
	if len(got) != 2 {
		t.Errorf("parsed %d results, want 2 (events without a Test name are ignored)", len(got))
	}
}

func TestBuildManifest_StatusesAndSummary(t *testing.T) {
	m := BuildManifest(sampleResults(), fixedTime)

	// All fifteen fast-gate capabilities pass (Wave 1 + Wave 2); the Tier N
	// rows are nightly.
	if m.Summary.Green != 15 {
		t.Errorf("Green = %d, want 15", m.Summary.Green)
	}
	if m.Summary.Failed != 0 {
		t.Errorf("Failed = %d, want 0", m.Summary.Failed)
	}
	if m.Summary.NightlyOnly < 1 {
		t.Errorf("NightlyOnly = %d, want at least 1 documented gap", m.Summary.NightlyOnly)
	}
	if m.Summary.Total != len(m.Capabilities) {
		t.Errorf("Total %d != len(capabilities) %d", m.Summary.Total, len(m.Capabilities))
	}

	// A Tier N capability must read as nightly with no measured runtime.
	for _, c := range m.Capabilities {
		if c.Tier == TierN && c.Status != StatusNightly {
			t.Errorf("Tier N capability %q status = %q, want nightly", c.ID, c.Status)
		}
	}
	if m.HasFastFailure() {
		t.Error("HasFastFailure() = true, want false when all fast tests pass")
	}
}

func TestBuildManifest_NotRunWhenTestMissing(t *testing.T) {
	// Empty results: every fast-gate capability is not-run, none failed.
	m := BuildManifest(map[string]testResult{}, fixedTime)
	if m.Summary.Green != 0 {
		t.Errorf("Green = %d, want 0 with no results", m.Summary.Green)
	}
	if m.Summary.NotCovered == 0 {
		t.Error("NotCovered = 0, want the fast-gate capabilities flagged not-run")
	}
	if m.HasFastFailure() {
		t.Error("a missing test is not-run, not a failure; HasFastFailure should be false")
	}
}

func TestBuildManifest_FastFailureBlocks(t *testing.T) {
	res := sampleResults()
	res["TestCapability_Agent_EchoRoundTrip"] = testResult{status: StatusFail, elapsed: 4.2}
	m := BuildManifest(res, fixedTime)
	if !m.HasFastFailure() {
		t.Error("HasFastFailure() = false, want true when a Tier F capability fails")
	}
	if m.Summary.Failed != 1 {
		t.Errorf("Failed = %d, want 1", m.Summary.Failed)
	}
}

func TestRenderDashboard_ContainsCapabilityContent(t *testing.T) {
	m := BuildManifest(sampleResults(), fixedTime)
	html, err := RenderDashboard(m)
	if err != nil {
		t.Fatalf("RenderDashboard: %v", err)
	}

	for _, want := range []string{
		"<!DOCTYPE html>",
		"agent-deck Capability E2E Dashboard",
		"Send a message to an agent and read its reply", // the backbone card title
		"Session lifecycle",                             // a group heading
		"Agent interaction",
		"PASS",
		"NIGHTLY",
		"TestCapability_Agent_EchoRoundTrip",
		"2026-05-26T12:00:00Z",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("dashboard missing expected content: %q", want)
		}
	}

	// Style discipline: no emoji, no em-dash separators.
	if strings.ContainsRune(html, '—') {
		t.Error("dashboard contains an em-dash; the style rules forbid em-dash separators")
	}
	for _, emoji := range []string{"✅", "❌", "⚠", "🔘", "💤", "💻", "🧪", "🌍", "🔑", "🌐"} {
		if strings.Contains(html, emoji) {
			t.Errorf("dashboard contains emoji %q; the style rules forbid emoji", emoji)
		}
	}
}

func TestAttachSnapshots_EmbedsContentByID(t *testing.T) {
	dir := t.TempDir()
	// One snapshot file named by capability id, plus a stale file with no
	// matching capability (must be ignored, not error).
	if err := os.WriteFile(filepath.Join(dir, "send-output-echo.txt"), []byte("$ send PING-42\nECHO:PING-42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "no-such-capability.txt"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := BuildManifest(sampleResults(), fixedTime)
	m.AttachSnapshots(dir)

	var echo *Capability
	for i := range m.Capabilities {
		if m.Capabilities[i].ID == "send-output-echo" {
			echo = &m.Capabilities[i]
		}
	}
	if echo == nil {
		t.Fatal("send-output-echo capability missing from registry")
	}
	if !strings.Contains(echo.Snapshot, "ECHO:PING-42") {
		t.Errorf("snapshot not attached to send-output-echo: %q", echo.Snapshot)
	}
	if !strings.Contains(echo.Snapshot, "send PING-42") {
		t.Errorf("snapshot should show the sent token: %q", echo.Snapshot)
	}

	// A capability without a snapshot file keeps an empty Snapshot.
	for _, c := range m.Capabilities {
		if c.ID == "fork-context" && c.Snapshot != "" {
			t.Errorf("capability %q with no snapshot file should stay empty, got %q", c.ID, c.Snapshot)
		}
	}
}

func TestAttachSnapshots_MissingDirIsNoop(t *testing.T) {
	m := BuildManifest(sampleResults(), fixedTime)
	// Must not panic or error when the directory does not exist; snapshots are
	// optional display data.
	m.AttachSnapshots(filepath.Join(t.TempDir(), "does-not-exist"))
	for _, c := range m.Capabilities {
		if c.Snapshot != "" {
			t.Errorf("capability %q should have no snapshot when dir is absent", c.ID)
		}
	}
}

func TestRenderDashboard_RendersTerminalBlockEscaped(t *testing.T) {
	dir := t.TempDir()
	// Deliberately include HTML-special characters and newlines so we can prove
	// the terminal block escapes them and preserves line breaks.
	raw := "$ session send cap-echo 'PING & <go>'\nECHO:PING & <go>\nECHOBOT READY"
	if err := os.WriteFile(filepath.Join(dir, "send-output-echo.txt"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	m := BuildManifest(sampleResults(), fixedTime)
	m.AttachSnapshots(dir)

	html, err := RenderDashboard(m)
	if err != nil {
		t.Fatalf("RenderDashboard: %v", err)
	}

	// The terminal block is present.
	if !strings.Contains(html, "terminal") {
		t.Error("dashboard should contain a terminal-styled snapshot block")
	}
	// HTML special characters are escaped, not injected raw.
	if !strings.Contains(html, "ECHO:PING &amp; &lt;go&gt;") {
		t.Errorf("snapshot must be HTML-escaped (&amp;/&lt;/&gt;); html did not contain the escaped echo line")
	}
	if strings.Contains(html, "ECHO:PING & <go>") {
		t.Error("dashboard contains the raw unescaped snapshot; HTML escaping is broken")
	}
	// A reader-facing label introduces the block.
	if !strings.Contains(html, "What the terminal showed") {
		t.Error("dashboard should label the terminal block for readers")
	}
}

func TestRenderDashboard_NoTerminalBlockWithoutSnapshot(t *testing.T) {
	// No snapshots attached: cards must not render an empty terminal block.
	html, err := RenderDashboard(BuildManifest(sampleResults(), fixedTime))
	if err != nil {
		t.Fatalf("RenderDashboard: %v", err)
	}
	if strings.Contains(html, "What the terminal showed") {
		t.Error("with no snapshots attached, no terminal block should render")
	}
}

func TestCleanSnapshot_StripsShellMOTDAndSpinnerNoise(t *testing.T) {
	// A raw bash pane capture: Ubuntu MOTD, bare prompts, and the shell-spawn
	// line, with one genuine line of agent-deck content in the middle.
	raw := strings.Join([]string{
		`To run a command as administrator (user "root"), use "sudo <command>".`,
		`See "man sudo_root" for details.`,
		``,
		`Welcome to Ubuntu 24.04 LTS`,
		` * Documentation:  https://help.ubuntu.com`,
		`Last login: Mon May 26 14:00:00 2026`,
		`ashesh-goplani@HOST:~/project$ bash -c 'bash'`,
		`RUNNING (1):`,
		`  cap-start shell   ~/project`,
		`ashesh-goplani@HOST:~/project$`,
		``,
		``,
		``,
	}, "\n")

	got := cleanSnapshot(raw)

	for _, banned := range []string{
		"sudo_root", "man sudo", "To run a command as administrator",
		"Welcome to Ubuntu", "* Documentation", "Last login:",
		"bash -c", "@HOST:",
	} {
		if strings.Contains(got, banned) {
			t.Errorf("cleanSnapshot leaked shell chrome %q:\n%s", banned, got)
		}
	}
	// The genuine agent-deck content survives.
	if !strings.Contains(got, "RUNNING (1):") || !strings.Contains(got, "cap-start shell") {
		t.Errorf("cleanSnapshot dropped meaningful content:\n%s", got)
	}
}

func TestCleanSnapshot_DropsBrailleSpinnerFrames(t *testing.T) {
	// The echo agent animates a braille spinner; redraw frames leave garbled
	// "working"/"...EADY >" fragments that must not reach the dashboard.
	raw := strings.Join([]string{
		"ECHOBOT READY > PING-42",
		"⠋ working",
		"⢸ workingEADY >",
		"ECHO:PING-42",
	}, "\n")

	got := cleanSnapshot(raw)

	if strings.Contains(got, "working") {
		t.Errorf("spinner frames survived cleanSnapshot:\n%s", got)
	}
	if !strings.Contains(got, "ECHOBOT READY > PING-42") || !strings.Contains(got, "ECHO:PING-42") {
		t.Errorf("cleanSnapshot dropped the real exchange:\n%s", got)
	}
}

func TestRenderDashboard_FailingCardUsesRed(t *testing.T) {
	res := sampleResults()
	res["TestCapability_Lifecycle_Add"] = testResult{status: StatusFail, elapsed: 2.0}
	html, err := RenderDashboard(BuildManifest(res, fixedTime))
	if err != nil {
		t.Fatalf("RenderDashboard: %v", err)
	}
	if !strings.Contains(html, `card red`) {
		t.Error("a failing capability should render a red card")
	}
	if !strings.Contains(html, "FAIL") {
		t.Error("a failing capability should show the FAIL label")
	}
}
