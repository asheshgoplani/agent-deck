package telemetry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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

// TestGrantChangesIdentityOnlyOnResetDeclineOrOff: once an install id exists,
// a yes keeps it and its salt. Only reset-id rotates them, and only a no or
// off clears them (the next yes then mints a new pair). A yes for a different
// destination keeps the id but never carries data recorded for the old one.
func TestGrantChangesIdentityOnlyOnResetDeclineOrOff(t *testing.T) {
	c := env(t)
	old := grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})

	s := LoadState()
	s.ConsentEndpoint = "https://other.example"
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID != old.InstallID || s.Salt != old.Salt {
		t.Fatal("a grant for a new destination must keep the install id and salt")
	}
	if len(spoolBytes(t)) != 0 || s.Daily != nil {
		t.Fatal("data recorded for another destination must never be sent to this one")
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}

	before := s.InstallID
	Decline(s, "9.9.9", c.now())
	if s.InstallID != "" || s.Salt != "" {
		t.Fatal("a no must clear the install id and salt")
	}
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID == "" || s.InstallID == before {
		t.Fatal("a grant after a no must mint a new install id")
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}

	before = s.InstallID
	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	off := LoadState()
	if off.InstallID != "" || off.Salt != "" || len(spoolBytes(t)) != 0 {
		t.Fatal("off must clear the install id, salt and spool")
	}
	again := grant(t, c)
	if again.InstallID == "" || again.InstallID == before {
		t.Fatal("a grant after off must mint a new install id")
	}

	reset, err := ResetID()
	if err != nil {
		t.Fatal(err)
	}
	if reset.InstallID == again.InstallID || reset.Salt == again.Salt {
		t.Fatal("reset-id must rotate the identity")
	}
}

// TestReconsentNote: the prompt says the id is kept exactly when a yes keeps
// an existing id, and says whether earlier unsent data is sent or deleted.
func TestReconsentNote(t *testing.T) {
	c := env(t)
	if note := ReconsentNote(LoadState(), Endpoint()); note != "" {
		t.Fatalf("fresh install: note %q", note)
	}
	grant(t, c)
	downgradeToSchema2(t)
	s := LoadState()
	if note := ReconsentNote(s, Endpoint()); note != PromptKeepsID {
		t.Fatalf("schema re-ask: note %q", note)
	}
	if note := ReconsentNote(s, "https://other.example"); note != PromptKeepsIDNewEndpoint {
		t.Fatalf("new destination: note %q", note)
	}
	Decline(s, "9.9.9", c.now())
	if note := ReconsentNote(s, Endpoint()); note != "" {
		t.Fatalf("after a no: note %q", note)
	}
	for _, note := range []string{PromptKeepsID, PromptKeepsIDNewEndpoint} {
		if len(note) > PromptWidth-6 {
			t.Fatalf("%q does not fit the prompt box", note)
		}
	}
}

// fixture122 is the state and spool the published 1.16.22 release wrote after
// a TUI grant (schema 2) and a few TUI and CLI starts, captured from the real
// binary in an isolated container. Its consent endpoint is pointed at the
// test's fake server, as if 1.16.22 had been granted for it.
type fixture122 struct {
	id, salt string
	uuids    map[string]bool // every spooled event uuid
	day      string          // the day 1.16.22 recorded on
}

func install1_16_22Grant(t *testing.T) fixture122 {
	t.Helper()
	dir := filepath.Join("testdata", "v1.16.22-granted")
	stateBody, err := os.ReadFile(filepath.Join(dir, "telemetry-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	spoolBody, err := os.ReadFile(filepath.Join(dir, "telemetry-spool.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(stateBody, &raw); err != nil {
		t.Fatal(err)
	}
	if raw["schema_version"] != float64(2) || raw["consent"] != "granted" {
		t.Fatalf("fixture is not a schema 2 grant: %v", raw)
	}
	raw["consent_endpoint"] = Endpoint()
	stateBody, err = json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	spool, err := spoolPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(sp), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp, stateBody, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spool, spoolBody, 0600); err != nil {
		t.Fatal(err)
	}
	f := fixture122{id: raw["install_id"].(string), salt: raw["salt"].(string), day: raw["consent_day"].(string), uuids: map[string]bool{}}
	for _, line := range strings.Split(strings.TrimSpace(string(spoolBody)), "\n") {
		var l spoolLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatal(err)
		}
		f.uuids[l.U] = true
	}
	return f
}

// TestReconsentAfter1_16_22GrantKeepsIdentityAndSendsSpool: state written by
// a real 1.16.22 grant, loaded by this build, is asked again; a yes keeps
// the install id and salt, keeps the unsent 1.16.22 spool, and the next
// day's upload sends those events under the same id.
func TestReconsentAfter1_16_22GrantKeepsIdentityAndSendsSpool(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	f := install1_16_22Grant(t)
	c.set(time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local))

	s := LoadState()
	if s.Consent != ConsentUndecided || s.Previous() != "v2_granted" || !ShouldPrompt(s) {
		t.Fatalf("a 1.16.22 grant must be asked again: consent=%s previous=%s", s.Consent, s.Previous())
	}
	if ok, _ := Enabled(s); ok {
		t.Fatal("a schema 2 grant enables recording before re-consent")
	}
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	after := LoadState()
	if after.InstallID != f.id || after.Salt != f.salt {
		t.Fatalf("re-consent changed the 1.16.22 identity: id %s -> %s", f.id, after.InstallID)
	}
	if ok, _ := Enabled(after); !ok {
		t.Fatal("re-consent must enable the current schema")
	}
	kept := 0
	for _, l := range spoolLines(t) {
		if f.uuids[l.U] {
			kept++
		}
	}
	if kept == 0 {
		t.Fatal("re-consent deleted the unsent 1.16.22 spool")
	}
	if after.Daily[f.day] == nil {
		t.Fatal("re-consent dropped the 1.16.22 daily rollup")
	}
	if r := MaybeUpload(t.Context()); r.Sent || fake.hits() != 0 {
		t.Fatalf("sent on the re-consent day: %+v", r)
	}

	c.set(time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local))
	if r := MaybeUpload(t.Context()); !r.Sent {
		t.Fatalf("upload after re-consent: %+v", r)
	}
	sent := 0
	for i := 0; i < fake.hits(); i++ {
		for _, ev := range fake.batch(t, i).Batch {
			if ev.DistinctID != f.id {
				t.Fatalf("%s sent under %s, not the 1.16.22 id %s", ev.Event, ev.DistinctID, f.id)
			}
			if f.uuids[ev.UUID] {
				sent++
				if ev.Properties["v"] != "1.16.22" || ev.Properties["day"] != f.day {
					t.Fatalf("1.16.22 event %s lost its version or day: %v", ev.Event, ev.Properties)
				}
			}
		}
	}
	if sent != kept {
		t.Fatalf("uploaded %d of the %d kept 1.16.22 events", sent, kept)
	}
	if len(spoolLines(t)) != 0 {
		t.Fatal("acknowledged events must leave the spool")
	}
}

// TestReconsentUnderKeptIDSendsBaselineOnce: onboard.baseline is sent once
// per install id. A yes that keeps the id (schema re-ask) records the new
// telemetry.consent but no second baseline; a yes with a new id records one.
func TestReconsentUnderKeptIDSendsBaselineOnce(t *testing.T) {
	c := env(t)
	grant(t, c)
	AfterConsent(SourceTUIFirstRun, "none", Baseline{}, nil)
	if n := countEvent(spoolLines(t), "onboard.baseline"); n != 1 {
		t.Fatalf("first consent: %d baselines", n)
	}
	downgradeToSchema2(t)
	c.set(at(1, 9, 0))
	s := LoadState()
	prev := s.Previous()
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	AfterConsent(SourceTUIFirstRun, prev, Baseline{}, nil)
	lines := spoolLines(t)
	if countEvent(lines, "onboard.baseline") != 1 || countEvent(lines, "telemetry.consent") != 2 {
		t.Fatalf("re-consent under a kept id: %v", eventNames(lines))
	}

	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	grant(t, c)
	AfterConsent(SourceCLIOn, "none", Baseline{}, nil)
	if n := countEvent(spoolLines(t), "onboard.baseline"); n != 1 {
		t.Fatalf("a new id after off must get its own baseline: %d", n)
	}
}

// TestRegrantForNewEndpointKeepsSequenceAndMilestones: a yes for another
// destination keeps the id, so it must not restart seq or forget reached
// milestones; otherwise an A, B, A switch repeats seq numbers and one-time
// milestones under the same distinct_id.
func TestRegrantForNewEndpointKeepsSequenceAndMilestones(t *testing.T) {
	c := env(t)
	grant(t, c)
	AfterConsent(SourceTUIFirstRun, "none", Baseline{}, nil)
	before := LoadState()
	if before.Seq == 0 || before.Milestones == 0 {
		t.Fatalf("setup: seq %d milestones %d", before.Seq, before.Milestones)
	}
	s := LoadState()
	s.ConsentEndpoint = "https://other.example"
	if err := Grant(s, "9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s.InstallID != before.InstallID || s.Seq != before.Seq || s.Milestones != before.Milestones {
		t.Fatalf("new destination: id kept=%v seq %d -> %d milestones %d -> %d",
			s.InstallID == before.InstallID, before.Seq, s.Seq, before.Milestones, s.Milestones)
	}
	if len(spoolBytes(t)) != 0 || s.Daily != nil {
		t.Fatal("data recorded for the old destination must be deleted")
	}
}
