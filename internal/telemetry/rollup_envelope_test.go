package telemetry

import "testing"

// TestRollupEnvelope: a day recorded by release X from the CLI by a coding
// agent and uploaded the next day by a TUI of release Y keeps v = X, and its
// rollups carry the neutral envelope (actor mixed, surface rollup) instead of
// a real-looking human/tui label. The true split stays in usage.daily.
func TestRollupEnvelope(t *testing.T) {
	c := env(t)
	grant(t, c)
	c.set(at(1, 10, 0))
	SetProcess("1.16.25", SurfaceCLI)
	t.Setenv("CLAUDECODE", "1")
	CLICommand(Feature("launch"), false)
	FeatureUsed(Feature("rename"), false)
	t.Setenv("CLAUDECODE", "")

	c.set(at(2, 10, 0))
	SetProcess("1.16.26", SurfaceTUI)
	s := LoadState()
	rollups := 0
	for _, p := range s.pending(spoolLines(t), c.now()) {
		if p.rollupDay == "" || p.ev.Event == "" {
			continue
		}
		rollups++
		pr := p.ev.Properties
		if pr["v"] != "1.16.25" {
			t.Errorf("%s: v = %v, want the recording release 1.16.25", p.ev.Event, pr["v"])
		}
		if pr["actor"] != "mixed" || pr["surface"] != "rollup" {
			t.Errorf("%s: actor/surface = %v/%v, want mixed/rollup", p.ev.Event, pr["actor"], pr["surface"])
		}
		if pr["os"] == nil || pr["arch"] == nil {
			t.Errorf("%s: missing os/arch", p.ev.Event)
		}
		if p.ev.Event == "usage.daily" && (pr["agent_hours"] == 0 || pr["human_hours"] != 0) {
			t.Errorf("usage.daily lost the actor split: human %v agent %v", pr["human_hours"], pr["agent_hours"])
		}
	}
	if rollups < 3 {
		t.Fatalf("want usage.daily and two feature.daily rollups, got %d", rollups)
	}
}

// TestRollupLegacyDayVersion: a day stored by an older client without a
// version falls back to the uploading release.
func TestRollupLegacyDayVersion(t *testing.T) {
	c := env(t)
	grant(t, c)
	s := LoadState()
	s.Daily = map[string]*DailyRollup{"2026-09-27": {CLICmds: 1}}
	c.set(at(2, 10, 0))
	SetProcess("1.16.26", SurfaceTUI)
	found := false
	for _, p := range s.pending(nil, c.now()) {
		found = found || p.ev.Event == "usage.daily"
		if p.ev.Event == "usage.daily" && p.ev.Properties["v"] != "1.16.26" {
			t.Errorf("legacy day: v = %v, want 1.16.26", p.ev.Properties["v"])
		}
	}
	if !found {
		t.Fatal("no usage.daily for the legacy day")
	}
}
