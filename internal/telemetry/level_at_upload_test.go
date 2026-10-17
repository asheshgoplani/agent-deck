package telemetry

import (
	"strings"
	"testing"
	"time"
)

// basicEvents are the detailed events level basic may send (install.tick has
// its own ledger and never goes through the spool).
var basicEvents = map[string]bool{"app.start": true, "usage.daily": true, "env.snapshot": true}

// assertBasicOnly fails when any uploaded detailed event carries data that
// level basic never sends, and returns how many events were checked.
func assertBasicOnly(t *testing.T, fake *fakePostHog) int {
	t.Helper()
	n := 0
	for i := 0; i < fake.hits(); i++ {
		for _, ev := range fake.batch(t, i).Batch {
			n++
			p := ev.Properties
			_, hour := p["hour_local"]
			_, weekday := p["weekday_local"]
			_, ds := p["ds_session"]
			if !basicEvents[ev.Event] || hour || weekday || ds || p["level"] != "basic" || !strings.HasSuffix(ev.Timestamp, "T12:00:00Z") {
				t.Errorf("sent at level basic: %s level=%v hour=%v weekday=%v ds=%v ts=%s",
					ev.Event, p["level"], p["hour_local"], p["weekday_local"], p["ds_session"], ev.Timestamp)
			}
		}
	}
	return n
}

// assertSpoolBasicOnly fails when a waiting spool line holds data that level
// basic never records.
func assertSpoolBasicOnly(t *testing.T) {
	t.Helper()
	for _, l := range spoolLines(t) {
		if _, ds := l.P["ds_session"]; !basicEvents[l.E] || l.H != nil || l.W != nil || ds || l.L != "basic" {
			t.Errorf("spool keeps full-level data at basic: %s h=%v w=%v p=%v", l.E, l.H, l.W, l.P)
		}
	}
}

// TestReconsentAtBasicSendsOnlyBasicData: a real 1.16.22 spool recorded at
// level full, re-consented while the level is basic (config ceiling or the
// stored level), sends only what level basic allows: the basic events,
// without hour, weekday or ds_session. Full-only lines are dropped unsent.
func TestReconsentAtBasicSendsOnlyBasicData(t *testing.T) {
	for _, how := range []string{"config", "stored"} {
		t.Run(how, func(t *testing.T) {
			c := env(t)
			fake := newFakePostHog(t)
			f := install1_16_22Grant(t)
			if how == "config" {
				SetConfigLevel("basic")
			}
			c.set(time.Date(2026, 10, 7, 9, 0, 0, 0, time.Local))
			s := LoadState()
			if how == "stored" {
				s.Level = LevelBasic
			}
			if err := Grant(s, "9.9.9", c.now()); err != nil {
				t.Fatal(err)
			}
			if err := SaveState(s); err != nil {
				t.Fatal(err)
			}
			if EffectiveLevel(LoadState()) != LevelBasic {
				t.Fatal("setup: level is not basic")
			}

			c.set(time.Date(2026, 10, 8, 9, 0, 0, 0, time.Local))
			if body, err := PreviewBatch(); err != nil || strings.Contains(string(joinBodies(body)), "hour_local") {
				t.Fatalf("preview at basic shows full-level data: %v %s", err, joinBodies(body))
			}
			if r := MaybeUpload(t.Context()); !r.Sent {
				t.Fatalf("upload: %+v", r)
			}
			assertBasicOnly(t, fake)
			kept := 0
			for i := 0; i < fake.hits(); i++ {
				for _, ev := range fake.batch(t, i).Batch {
					if f.uuids[ev.UUID] {
						kept++
						if ev.DistinctID != f.id {
							t.Fatalf("%s sent under another id", ev.Event)
						}
					}
				}
			}
			if kept == 0 {
				t.Fatal("the basic 1.16.22 events (app.start, env.snapshot) must still be sent")
			}
			if n := len(spoolLines(t)); n != 0 {
				t.Fatalf("%d full-only lines stay in the spool", n)
			}
		})
	}
}

// TestLoweringLevelAppliesToWaitingSpool: within one grant, lines recorded at
// full and still waiting when the level drops to basic (stored or config
// ceiling) are reduced to what basic allows, in preview and upload alike.
func TestLoweringLevelAppliesToWaitingSpool(t *testing.T) {
	for _, how := range []string{"stored", "config"} {
		t.Run(how, func(t *testing.T) {
			c := env(t)
			fake := newFakePostHog(t)
			grant(t, c)
			c.set(at(1, 9, 0))
			SessionCreated(SessionCreateInfo{Tool: "claude", Via: ViaTUINew, SessionID: "s1"})
			withState(func(s *State, now time.Time) bool {
				s.appStartLocked(FleetCounts{Sessions: 1}, now)
				return true
			})
			if countEvent(spoolLines(t), "session.create") != 1 || countEvent(spoolLines(t), "app.start") != 1 {
				t.Fatalf("setup: %v", eventNames(spoolLines(t)))
			}
			if how == "stored" {
				if _, err := SetLevel(LevelBasic); err != nil {
					t.Fatal(err)
				}
			} else {
				SetConfigLevel("basic")
			}

			c.set(at(2, 9, 0))
			bodies, err := PreviewBatch()
			if err != nil {
				t.Fatal(err)
			}
			for _, b := range bodies {
				if strings.Contains(string(b), "session.create") || strings.Contains(string(b), "hour_local") || strings.Contains(string(b), "ds_session") {
					t.Fatalf("preview at basic shows full-level data: %s", b)
				}
			}
			if r := MaybeUpload(t.Context()); !r.Sent {
				t.Fatalf("upload: %+v", r)
			}
			if assertBasicOnly(t, fake) == 0 {
				t.Fatal("app.start recorded at full must still be sent at basic")
			}
			assertSpoolBasicOnly(t)
		})
	}
}

func joinBodies(bodies [][]byte) []byte {
	var out []byte
	for _, b := range bodies {
		out = append(out, b...)
	}
	return out
}
