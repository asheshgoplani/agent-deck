package statedb

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func fireRow(id, hash string, ts time.Time) TriggerFireRow {
	return TriggerFireRow{TriggerID: id, TS: ts, Target: "inbox", Outcome: "delivered", Line: "tick", TextHash: hash}
}

// Fires older than the retention window are dropped on the next insert.
func TestTriggerFiresRetentionByAge(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	if _, err := db.RecordTriggerFire(fireRow("systemd:poll", "a", now.Add(-TriggerFireRetention-time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordTriggerFire(fireRow("launchd:other", "b", now.Add(-8*24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordTriggerFire(fireRow("systemd:poll", "c", now)); err != nil {
		t.Fatal(err)
	}
	fires, err := db.ListTriggerFires("systemd:poll", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != 1 || fires[0].TextHash != "c" {
		t.Fatalf("fires after age prune = %+v, want only the fresh one", fires)
	}
	// The age bound is global: another trigger's stale fire goes too.
	if other, _ := db.ListTriggerFires("launchd:other", 100); len(other) != 0 {
		t.Fatalf("stale fire of another trigger kept: %+v", other)
	}
}

// At most TriggerFireMaxPerTrigger fires are kept per trigger, newest first;
// another trigger's log is not touched by the count bound.
func TestTriggerFiresRetentionByCount(t *testing.T) {
	db := openTestDB(t)
	base := time.Now().Add(-time.Hour)
	tx, err := db.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < TriggerFireMaxPerTrigger+5; i++ {
		if _, err := tx.Exec(`INSERT INTO trigger_fires (trigger_id, ts, outcome, text_hash) VALUES (?, ?, 'delivered', ?)`,
			"systemd:busy", base.Add(time.Duration(i)*time.Second).Unix(), fmt.Sprintf("h%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO trigger_fires (trigger_id, ts, outcome) VALUES ('systemd:quiet', ?, 'delivered')`, base.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RecordTriggerFire(fireRow("systemd:busy", "last", time.Now())); err != nil {
		t.Fatal(err)
	}
	fires, err := db.ListTriggerFires("systemd:busy", 5000)
	if err != nil {
		t.Fatal(err)
	}
	if len(fires) != TriggerFireMaxPerTrigger {
		t.Fatalf("kept %d fires, want %d", len(fires), TriggerFireMaxPerTrigger)
	}
	if fires[0].TextHash != "last" || fires[len(fires)-1].TextHash != "h6" {
		t.Fatalf("kept the wrong fires: newest %q oldest %q", fires[0].TextHash, fires[len(fires)-1].TextHash)
	}
	if quiet, _ := db.ListTriggerFires("systemd:quiet", 10); len(quiet) != 1 {
		t.Fatalf("count bound pruned another trigger: %d", len(quiet))
	}
}

// A fire repeating the previous fire's text hash of the same trigger is a
// no-op; an empty hash never is; another trigger's hash does not count.
func TestTriggerFireNoopDetection(t *testing.T) {
	db := openTestDB(t)
	now := time.Now()
	steps := []struct {
		id, hash string
		want     bool
	}{
		{"systemd:poll", "x", false},
		{"systemd:poll", "x", true},
		{"systemd:poll", "y", false},
		{"launchd:other", "y", false},
		{"systemd:poll", "y", true},
		{"systemd:poll", "", false},
		{"systemd:poll", "", false},
	}
	for i, s := range steps {
		got, err := db.RecordTriggerFire(fireRow(s.id, s.hash, now.Add(time.Duration(i)*time.Second)))
		if err != nil {
			t.Fatal(err)
		}
		if got.Noop != s.want {
			t.Fatalf("step %d (%s %q): noop=%v want %v", i, s.id, s.hash, got.Noop, s.want)
		}
	}
	stats, err := db.TriggerStatsSince(now.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st := stats["systemd:poll"]; st == nil || st.Fires24h != 6 || st.Noops24h != 2 || st.LastFire == nil {
		t.Fatalf("stats = %+v", st)
	}
}

// A rescan keeps rows that were pushed (only losing their scan columns),
// drops scan-only rows that vanished, and never clears a mute.
func TestReplaceScannedTriggersKeepsPushedRowsAndMutes(t *testing.T) {
	db := openTestDB(t)
	scan := func(rows ...TriggerRow) {
		t.Helper()
		if err := db.ReplaceScannedTriggers(rows); err != nil {
			t.Fatal(err)
		}
	}
	scan(
		TriggerRow{ID: "systemd:both", Kind: "systemd", Source: "both", ConfigPath: "/u/both.service", Schedule: "every 5m", Unit: "both", Target: "inbox", TargetBy: "title"},
		TriggerRow{ID: "systemd:scanonly", Kind: "systemd", Source: "scanonly", Unit: "scanonly"},
		TriggerRow{ID: "launchd:kept", Kind: "launchd", Source: "kept", Unit: "kept"},
	)
	if err := db.UpsertPushedTrigger(TriggerRow{ID: "systemd:both", Kind: "systemd", Source: "both", Target: "other", TargetBy: "title", Verb: "send"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPushedTrigger(TriggerRow{ID: "heartbeat:ops", Kind: "heartbeat", Source: "ops", Target: "conductor-ops", Verb: "send"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Hour).Truncate(time.Second)
	for _, id := range []string{"systemd:both", "launchd:kept"} {
		if ok, err := db.SetTriggerMute(id, until); err != nil || !ok {
			t.Fatalf("mute %s: %v %v", id, ok, err)
		}
	}
	if _, err := db.RecordTriggerFire(fireRow("systemd:scanonly", "z", time.Now())); err != nil {
		t.Fatal(err)
	}

	both, _ := db.GetTrigger("systemd:both")
	if both == nil || both.Target != "inbox" || !both.Pushed || !both.Scanned {
		t.Fatalf("push over a scanned row must keep the scanned target: %+v", both)
	}

	// Second scan: "both" is gone from disk, "scanonly" too, "kept" stays.
	scan(TriggerRow{ID: "launchd:kept", Kind: "launchd", Source: "kept", Unit: "kept", Schedule: "daily 07:00"})

	both, _ = db.GetTrigger("systemd:both")
	if both == nil || both.Scanned || !both.Pushed || both.ConfigPath != "" || both.Schedule != "" || both.Unit != "" {
		t.Fatalf("pushed row after its unit vanished: %+v", both)
	}
	if !both.MutedUntil.Equal(until) {
		t.Fatalf("rescan cleared the mute of a pushed row: %v", both.MutedUntil)
	}
	if gone, _ := db.GetTrigger("systemd:scanonly"); gone != nil {
		t.Fatalf("scan-only row that vanished was kept: %+v", gone)
	}
	if fires, _ := db.ListTriggerFires("systemd:scanonly", 10); len(fires) != 0 {
		t.Fatalf("fires of a deleted trigger kept: %d", len(fires))
	}
	kept, _ := db.GetTrigger("launchd:kept")
	if kept == nil || !kept.Scanned || kept.Schedule != "daily 07:00" || !kept.MutedUntil.Equal(until) {
		t.Fatalf("rescanned row: %+v", kept)
	}
	if hb, _ := db.GetTrigger("heartbeat:ops"); hb == nil || !hb.Pushed {
		t.Fatalf("push-only row dropped by a scan: %+v", hb)
	}
}

func TestSetTriggerMute(t *testing.T) {
	db := openTestDB(t)
	if ok, err := db.SetTriggerMute("systemd:missing", time.Now().Add(time.Hour)); err != nil || ok {
		t.Fatalf("mute of an unknown trigger: ok=%v err=%v", ok, err)
	}
	if err := db.UpsertPushedTrigger(TriggerRow{ID: "cron:poll.sh", Kind: "cron", Source: "poll.sh"}); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(24 * time.Hour).Truncate(time.Second)
	if ok, err := db.SetTriggerMute("cron:poll.sh", until); err != nil || !ok {
		t.Fatalf("mute: %v %v", ok, err)
	}
	row, _ := db.GetTrigger("cron:poll.sh")
	if !row.MutedUntil.Equal(until) {
		t.Fatalf("muted_until = %v, want %v", row.MutedUntil, until)
	}
	// A later push does not unmute.
	if err := db.UpsertPushedTrigger(TriggerRow{ID: "cron:poll.sh", Kind: "cron", Source: "poll.sh"}); err != nil {
		t.Fatal(err)
	}
	if row, _ = db.GetTrigger("cron:poll.sh"); !row.MutedUntil.Equal(until) {
		t.Fatalf("push cleared the mute: %v", row.MutedUntil)
	}
	if ok, err := db.SetTriggerMute("cron:poll.sh", time.Time{}); err != nil || !ok {
		t.Fatalf("unmute: %v %v", ok, err)
	}
	if row, _ = db.GetTrigger("cron:poll.sh"); !row.MutedUntil.IsZero() {
		t.Fatalf("unmute left %v", row.MutedUntil)
	}
}

// Every trigger write moves triggers_rev so live readers refresh.
func TestTriggersRevBumps(t *testing.T) {
	db := openTestDB(t)
	rev := func() string {
		t.Helper()
		v, err := db.TriggersRev()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if rev() != "" {
		t.Fatal("fresh database has a triggers_rev")
	}
	seen := map[string]bool{"": true}
	step := func(name string, fn func() error) {
		t.Helper()
		time.Sleep(2 * time.Millisecond)
		if err := fn(); err != nil {
			t.Fatal(err)
		}
		r := rev()
		if seen[r] {
			t.Fatalf("%s did not bump triggers_rev (%q)", name, r)
		}
		seen[r] = true
	}
	_ = db.UpsertPushedTrigger(TriggerRow{ID: "watcher:gh", Kind: "watcher", Source: "gh"})
	step("fire", func() error { _, err := db.RecordTriggerFire(fireRow("watcher:gh", "a", time.Now())); return err })
	step("mute", func() error { _, err := db.SetTriggerMute("watcher:gh", time.Now().Add(time.Hour)); return err })
	step("unmute", func() error { _, err := db.SetTriggerMute("watcher:gh", time.Time{}); return err })
	step("scan", db.BumpTriggersRev)
}

// A DB made by the first triggers build (#2738 @ 92556636) has a triggers
// table without unit_kind. Migrate adds it, and the old row stays readable.
func TestTriggersMigrateAddsUnitKindToFirstBuildTable(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.db.Exec(`CREATE TABLE triggers (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, source TEXT NOT NULL,
		host TEXT NOT NULL DEFAULT '', config_path TEXT NOT NULL DEFAULT '', verb TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '', target_by TEXT NOT NULL DEFAULT '', target_profile TEXT NOT NULL DEFAULT '',
		schedule TEXT NOT NULL DEFAULT '', unit TEXT NOT NULL DEFAULT '',
		pushed INTEGER NOT NULL DEFAULT 0, scanned INTEGER NOT NULL DEFAULT 0, inferred INTEGER NOT NULL DEFAULT 0,
		muted_until INTEGER NOT NULL DEFAULT 0, created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`INSERT INTO triggers (id, kind, source, unit, scanned, created_at, updated_at)
		VALUES ('systemd:poll', 'systemd', 'poll', 'poll', 1, 1, 1)`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("Migrate on a first-build triggers table: %v", err)
	}
	rows, err := db.ListTriggers()
	if err != nil {
		t.Fatalf("ListTriggers after migrate: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "systemd:poll" || rows[0].UnitKind != "" {
		t.Fatalf("rows = %+v", rows)
	}
	if err := db.ReplaceScannedTriggers([]TriggerRow{{ID: "systemd:poll", Kind: "systemd", Source: "poll", Unit: "poll", UnitKind: "systemd"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(); err != nil { // idempotent on the next open
		t.Fatal(err)
	}
	if r, err := db.GetTrigger("systemd:poll"); err != nil || r == nil || r.UnitKind != "systemd" {
		t.Fatalf("row after rescan = %+v %v", r, err)
	}
}
