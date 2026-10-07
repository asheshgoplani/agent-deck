package telemetry

import (
	"runtime"
	"testing"
)

// downgradeToSchema2 rewrites the saved grant as the schema-2 build left it.
func downgradeToSchema2(t *testing.T) {
	t.Helper()
	s := LoadState()
	s.SchemaVersion = 2
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
}

// TestSchemaReconsentKeepsIdentityAndSpool: an install granted under schema 2
// that upgrades and answers the re-prompt with yes keeps its install id,
// salt, unsent spool and rollups, and later uploads the pre-upgrade events
// under the same id with os, arch and version.
func TestSchemaReconsentKeepsIdentityAndSpool(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	old := grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	downgradeToSchema2(t)
	day0 := dayOf(c.now())

	c.set(at(1, 9, 0))
	migrated := LoadState()
	if migrated.Consent != ConsentUndecided || migrated.Previous() != "v2_granted" {
		t.Fatalf("schema-2 grant must be re-asked: consent=%s previous=%s", migrated.Consent, migrated.Previous())
	}
	if ok, _ := Enabled(migrated); ok {
		t.Fatal("a schema-2 grant enables schema 3 before re-consent")
	}
	if err := Grant(migrated, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(migrated); err != nil {
		t.Fatal(err)
	}
	after := LoadState()
	if after.InstallID != old.InstallID || after.Salt != old.Salt {
		t.Fatal("re-consent after a schema bump rotated the install id or salt")
	}
	if countEvent(spoolLines(t), "session.end") != 1 {
		t.Fatalf("re-consent dropped the unsent spool: %v", eventNames(spoolLines(t)))
	}
	if after.Daily[day0] == nil {
		t.Fatal("re-consent dropped the unsent rollups")
	}
	if ok, _ := Enabled(after); !ok || after.SchemaVersion != SchemaVersion {
		t.Fatal("re-consent must enable the current schema")
	}

	c.set(at(2, 9, 0))
	if r := MaybeUpload(t.Context()); !r.Sent || fake.hits() != 1 {
		t.Fatalf("upload after re-consent: %+v hits %d", r, fake.hits())
	}
	var sawOld bool
	for _, ev := range fake.batch(t, 0).Batch {
		p := ev.Properties
		if ev.DistinctID != old.InstallID {
			t.Fatalf("%s sent under a different id", ev.Event)
		}
		if p["os"] != runtime.GOOS || p["arch"] != runtime.GOARCH || p["v"] != "9.9.9" || p["schema"] != float64(SchemaVersion) {
			t.Fatalf("%s lacks os/arch/version/schema: %v", ev.Event, p)
		}
		if ev.Event == "session.end" && p["day"] == day0 {
			sawOld = true
		}
	}
	if !sawOld {
		t.Fatal("the pre-upgrade event was never uploaded")
	}
}

// TestGrantRotatesOnlyOnExplicitResetOrNewDestination: the identity still
// changes on a different endpoint, after a decline, and on reset-id.
func TestGrantRotatesOnlyOnExplicitResetOrNewDestination(t *testing.T) {
	c := env(t)
	old := grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})

	s := LoadState()
	s.ConsentEndpoint = "https://other.example"
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == old.InstallID || s.Salt == old.Salt || len(spoolBytes(t)) != 0 || s.Daily != nil {
		t.Fatal("a grant for a new destination must rotate the identity and drop local data")
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}

	before := s.InstallID
	Decline(s, "9.9.9", c.now())
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == "" || s.InstallID == before {
		t.Fatal("a grant after a decline must mint a new install id")
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}

	reset, err := ResetID()
	if err != nil {
		t.Fatal(err)
	}
	if reset.InstallID == s.InstallID || reset.Salt == s.Salt {
		t.Fatal("reset-id must rotate the identity")
	}
}
