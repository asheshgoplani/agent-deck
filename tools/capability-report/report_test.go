package main

import (
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

	// Eight fast-gate capabilities pass; the Tier N rows are nightly.
	if m.Summary.Green != 8 {
		t.Errorf("Green = %d, want 8", m.Summary.Green)
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
