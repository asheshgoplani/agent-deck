package telemetry

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/managed"
)

func managedLevelResult(level string) managed.Result {
	return managed.Result{Present: true, Config: managed.Config{TelemetryLevel: level}}
}

// TestManagedLevelIsACeiling: a managed "basic" lowers full to basic, a
// managed "full" never raises a person's basic, and "off" or an unreadable
// managed file is a hard off that consent cannot override.
func TestManagedLevelIsACeiling(t *testing.T) {
	c := env(t)
	grant(t, c)

	SetManaged(managedLevelResult(managed.LevelBasic))
	if got := EffectiveLevel(LoadState()); got != LevelBasic {
		t.Fatalf("managed basic: level %s, want basic", got)
	}
	if ManagedLevelCeiling() != managed.LevelBasic {
		t.Fatalf("ceiling %q", ManagedLevelCeiling())
	}

	SetManaged(managedLevelResult(managed.LevelFull))
	if _, err := SetLevel(LevelBasic); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveLevel(LoadState()); got != LevelBasic {
		t.Fatalf("managed full raised the person's basic to %s", got)
	}
	if _, err := SetLevel(LevelFull); err != nil {
		t.Fatal(err)
	}
	if got := EffectiveLevel(LoadState()); got != LevelFull {
		t.Fatalf("managed full with person full: %s", got)
	}

	SetManaged(managedLevelResult(managed.LevelOff))
	if ok, r := Enabled(LoadState()); ok || r != ReasonManaged {
		t.Fatalf("managed off: enabled=%v reason=%q", ok, r)
	}
	if ShouldPrompt(LoadState()) {
		t.Fatal("managed off still prompts")
	}

	SetManaged(managed.Result{Present: true, Err: errors.New("bad json")})
	if ok, r := Enabled(LoadState()); ok || r != ReasonManagedError {
		t.Fatalf("unreadable managed file: enabled=%v reason=%q", ok, r)
	}

	SetManaged(managed.Result{})
	if ok, _ := Enabled(LoadState()); !ok {
		t.Fatal("no managed file must leave the person's consent in force")
	}
}

// TestManagedConfigNeverGrants: a managed file alone, with no consent,
// records and sends nothing.
func TestManagedConfigNeverGrants(t *testing.T) {
	env(t)
	SetManaged(managedLevelResult(managed.LevelFull))
	SetOrgID("pilot-7f3k2", "")
	if ok, r := Enabled(LoadState()); ok || r != ReasonUndecided {
		t.Fatalf("managed full without consent: enabled=%v reason=%q", ok, r)
	}
}

func TestSetOrgID(t *testing.T) {
	env(t)
	cases := []struct{ managedID, configID, wantID, wantSrc string }{
		{"pilot-7f3k2", "other-org", "pilot-7f3k2", OrgSourceManaged},
		{"", "other-org", "other-org", OrgSourceConfig},
		{"", "", "", OrgSourceNone},
		{"", "alice@example.com", "", OrgSourceConfig + " (invalid, ignored)"},
		{"", "Alice Smith", "", OrgSourceConfig + " (invalid, ignored)"},
	}
	for _, tc := range cases {
		SetOrgID(tc.managedID, tc.configID)
		if OrgID() != tc.wantID || OrgSource() != tc.wantSrc {
			t.Errorf("SetOrgID(%q, %q): got %q/%q, want %q/%q", tc.managedID, tc.configID, OrgID(), OrgSource(), tc.wantID, tc.wantSrc)
		}
	}
}

// TestOrgPropertyOnlyWhenSet: detailed events carry "org" exactly when an
// org id is set, and stay personless either way.
func TestOrgPropertyOnlyWhenSet(t *testing.T) {
	c := env(t)
	grant(t, c)
	TUIStarted(FleetCounts{Sessions: 1}, true)
	c.set(at(1, 9, 0))
	t.Setenv(EnvPostHogKey, testKey)

	events := func() []phEvent {
		bodies, err := PreviewBatch()
		if err != nil || len(bodies) == 0 {
			t.Fatalf("preview: %v, %d bodies", err, len(bodies))
		}
		var b phBatch
		if err := json.Unmarshal(bodies[0], &b); err != nil {
			t.Fatal(err)
		}
		return b.Batch
	}

	for _, ev := range events() {
		if _, ok := ev.Properties["org"]; ok {
			t.Fatalf("%s carries org without an org id", ev.Event)
		}
	}
	SetOrgID("pilot-7f3k2", "")
	evs := events()
	for _, ev := range evs {
		if ev.Properties["org"] != "pilot-7f3k2" {
			t.Fatalf("%s: org = %v, want pilot-7f3k2", ev.Event, ev.Properties["org"])
		}
		if ev.Properties["$process_person_profile"] != false {
			t.Fatalf("%s lost $process_person_profile:false", ev.Event)
		}
		if _, ok := ev.Properties["$groups"]; ok {
			t.Fatalf("%s carries $groups", ev.Event)
		}
	}
}
