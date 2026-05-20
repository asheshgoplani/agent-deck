// Issue #924 — per-session named account slot (Option 1 MVP).
//
// These tests lock the schema migration, persistence round-trip, and
// SetField surface for the new Instance.Account field. The companion
// resolution + restart tests live in issue924_account_switch_test.go.
//
// Bug reporter: @bautrey. Design discussion: github issue #924 (Option 1
// — lightweight per-session profile slot, no Claude-side changes; the
// in-flight conversation is intentionally lost on switch).
package session

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// TestPersistence_Account_RoundTrip locks that an instance's Account
// field survives Save → Load via both LoadWithGroups (the canonical
// path) and LoadLite (the CLI fast-path).
func TestPersistence_Account_RoundTrip(t *testing.T) {
	s := newTestStorage(t)

	instances := []*Instance{
		{
			ID:          "with-account",
			Title:       "work-session",
			ProjectPath: "/tmp/p1",
			GroupPath:   "grp",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
			Account:     "work",
		},
		{
			ID:          "no-account",
			Title:       "default-session",
			ProjectPath: "/tmp/p2",
			GroupPath:   "grp",
			Command:     "claude",
			Tool:        "claude",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
		},
	}

	if err := s.SaveWithGroups(instances, nil); err != nil {
		t.Fatalf("SaveWithGroups failed: %v", err)
	}

	loaded, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups failed: %v", err)
	}
	byID := map[string]*Instance{}
	for _, inst := range loaded {
		byID[inst.ID] = inst
	}
	if got := byID["with-account"].Account; got != "work" {
		t.Errorf("with-account.Account = %q, want %q (lost on round-trip)", got, "work")
	}
	if got := byID["no-account"].Account; got != "" {
		t.Errorf("no-account.Account = %q, want empty (default must not leak)", got)
	}

	// LoadLite is the fast-path used by every CLI subcommand — it must
	// surface Account too or `session set` reads would see stale state.
	lite, _, err := s.LoadLite()
	if err != nil {
		t.Fatalf("LoadLite failed: %v", err)
	}
	liteByID := map[string]*InstanceData{}
	for _, d := range lite {
		liteByID[d.ID] = d
	}
	if got := liteByID["with-account"].Account; got != "work" {
		t.Errorf("LoadLite with-account.Account = %q, want %q", got, "work")
	}
	if got := liteByID["no-account"].Account; got != "" {
		t.Errorf("LoadLite no-account.Account = %q, want empty", got)
	}
}

// TestPersistence_Account_SchemaMigration locks the v9 ALTER migration:
// a DB created at the pre-#924 schema and then upgraded MUST gain the
// `account` column with default ” so legacy rows load cleanly.
func TestPersistence_Account_SchemaMigration(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	// Open + migrate twice — the second open simulates an upgrade. With
	// the alterMigrations entry being idempotent (duplicate-column
	// ignore) and the v9 version-gated migration also being idempotent,
	// both must succeed without error.
	for i := 0; i < 2; i++ {
		db, err := statedb.Open(dbPath)
		if err != nil {
			t.Fatalf("open pass %d: %v", i, err)
		}
		if err := db.Migrate(); err != nil {
			t.Fatalf("migrate pass %d: %v", i, err)
		}
		// Sanity check: save and reload a row carrying Account to confirm
		// the column is functionally present (not just declared).
		row := &statedb.InstanceRow{
			ID:          "m1",
			Title:       "migrated",
			ProjectPath: "/tmp/m",
			GroupPath:   "grp",
			Command:     "claude",
			Tool:        "claude",
			Status:      "idle",
			CreatedAt:   time.Now(),
			Account:     "work",
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("SaveInstance pass %d: %v", i, err)
		}
		rows, err := db.LoadInstances()
		if err != nil {
			t.Fatalf("LoadInstances pass %d: %v", i, err)
		}
		if len(rows) != 1 {
			t.Fatalf("pass %d: expected 1 row, got %d", i, len(rows))
		}
		if rows[0].Account != "work" {
			t.Errorf("pass %d: row.Account = %q, want %q", i, rows[0].Account, "work")
		}
		db.Close()
	}
}

// TestSetField_Account_RoundTrip locks that the session-set field name
// "account" (FieldAccount) hits the right struct field, normalises
// whitespace, and that the empty string clears the slot.
func TestSetField_Account_RoundTrip(t *testing.T) {
	inst := &Instance{ID: "x", Tool: "claude"}

	old, _, err := SetField(inst, FieldAccount, "  work  ", nil)
	if err != nil {
		t.Fatalf("SetField(account=work) returned err: %v", err)
	}
	if old != "" {
		t.Errorf("first set: oldValue = %q, want empty", old)
	}
	if inst.Account != "work" {
		t.Errorf("Account after set = %q, want %q (whitespace not trimmed)", inst.Account, "work")
	}

	old, _, err = SetField(inst, FieldAccount, "personal", nil)
	if err != nil {
		t.Fatalf("SetField(account=personal) returned err: %v", err)
	}
	if old != "work" {
		t.Errorf("second set: oldValue = %q, want %q", old, "work")
	}
	if inst.Account != "personal" {
		t.Errorf("Account = %q, want %q", inst.Account, "personal")
	}

	// Empty clears the override — restart-required policy means the
	// next start falls back to conductor/group/env (validated in the
	// switch test).
	if _, _, err := SetField(inst, FieldAccount, "", nil); err != nil {
		t.Fatalf("SetField(account=\"\") returned err: %v", err)
	}
	if inst.Account != "" {
		t.Errorf("Account after clear = %q, want empty", inst.Account)
	}
}

// TestSetField_Account_IsRestartRequired locks the restart policy:
// switching the account dir mid-session means the new account's
// settings.json / history live elsewhere, so the running conversation
// cannot continue without a restart. This is the Option 1 MVP tradeoff.
func TestSetField_Account_IsRestartRequired(t *testing.T) {
	if got := RestartPolicyFor(FieldAccount); got != FieldRestartRequired {
		t.Errorf("RestartPolicyFor(%q) = %v, want FieldRestartRequired — switching account changes CLAUDE_CONFIG_DIR and loses in-flight conversation", FieldAccount, got)
	}
}

// TestValidMutableFields_IncludesAccount locks that the field is
// advertised through the canonical mutable-field list (the CLI usage
// message and TUI edit dialog read from this slice — without it, the
// flag is invisible).
func TestValidMutableFields_IncludesAccount(t *testing.T) {
	for _, f := range ValidMutableFields {
		if f == FieldAccount {
			return
		}
	}
	t.Errorf("FieldAccount missing from ValidMutableFields; CLI/TUI surfaces won't list it")
}
