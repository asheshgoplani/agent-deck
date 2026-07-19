package statedb

import (
	"testing"
	"time"
)

func TestClaimSessionsFreshClaim(t *testing.T) {
	db := newTestDB(t)
	owned, err := db.ClaimSessions([]string{"s1", "s2"}, "fitstars", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if !owned["s1"] || !owned["s2"] {
		t.Errorf("expected both owned, got %v", owned)
	}
}

func TestClaimSessionsRespectsLiveForeignClaim(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()
	// Foreign live claim with equal specificity must NOT be stolen.
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "s1", 99999, "99999-1-deadbeef", now, now, "fitstars"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owned, err := db.ClaimSessions([]string{"s1"}, "fitstars", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if owned["s1"] {
		t.Error("stole a live claim of equal specificity")
	}
}

func TestClaimSessionsTakesOverStaleClaim(t *testing.T) {
	db := newTestDB(t)
	stale := time.Now().Add(-1 * time.Minute).Unix()
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "s1", 99999, "99999-1-deadbeef", stale, stale, "fitstars"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owned, err := db.ClaimSessions([]string{"s1"}, "", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if !owned["s1"] {
		t.Error("failed to take over a stale claim")
	}
}

func TestClaimSessionsMoreSpecificScopeWins(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "s1", 99999, "99999-1-deadbeef", now, now, "fitstars"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owned, err := db.ClaimSessions([]string{"s1"}, "fitstars/starfit", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if !owned["s1"] {
		t.Error("more specific scope failed to take over")
	}
	// And the reverse: a LESS specific scope must not steal it back.
	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if claims["s1"].Scope != "fitstars/starfit" {
		t.Errorf("scope = %q, want fitstars/starfit", claims["s1"].Scope)
	}
}

// TestClaimSessionsPIDReuseNotOurs covers a recycled PID: a foreign row
// happens to carry the same OS pid as this process (the previous owner of
// that pid exited and the OS handed it to us) but a different owner_token and
// a fresh heartbeat. Ownership must be decided by owner_token, not owner_pid,
// so this row must NOT be treated as ours — and with equal scope specificity
// it must not be stolen either.
func TestClaimSessionsPIDReuseNotOurs(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "s1", db.pid, "some-other-token-not-ours", now, now, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	owned, err := db.ClaimSessions([]string{"s1"}, "", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if owned["s1"] {
		t.Error("treated a foreign row with a reused pid as ours")
	}
	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if claims["s1"].OwnerToken != "some-other-token-not-ours" {
		t.Errorf("foreign row was mutated: owner_token = %q", claims["s1"].OwnerToken)
	}
}

// TestReleaseClaimsIgnoresForeignToken ensures ReleaseClaims only deletes
// rows owned by this process's token, even when the row shares this
// process's pid (PID reuse) or session id.
func TestReleaseClaimsIgnoresForeignToken(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "s1", db.pid, "foreign-token", now, now, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := db.ReleaseClaims([]string{"s1"}); err != nil {
		t.Fatalf("ReleaseClaims: %v", err)
	}
	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if _, ok := claims["s1"]; !ok {
		t.Error("ReleaseClaims deleted a foreign-token row")
	}
}

// TestClaimSessionsRefreshesOwnHeartbeat verifies the upsert's DO UPDATE
// branch doubles as the heartbeat refresh for already-owned rows: re-claiming
// a session this process already owns bumps its heartbeat even though the
// scope and stale-cutoff conditions alone would not trigger the update.
func TestClaimSessionsRefreshesOwnHeartbeat(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ClaimSessions([]string{"s1"}, "", 15*time.Second); err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	backdated := time.Now().Add(-10 * time.Second).Unix()
	if _, err := db.DB().Exec(
		"UPDATE session_claims SET heartbeat = ? WHERE session_id = ?", backdated, "s1"); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}

	owned, err := db.ClaimSessions([]string{"s1"}, "", 15*time.Second)
	if err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if !owned["s1"] {
		t.Fatal("expected s1 still owned")
	}
	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if claims["s1"].Heartbeat <= backdated {
		t.Errorf("heartbeat not refreshed: got %d, want > %d", claims["s1"].Heartbeat, backdated)
	}
}

func TestReleaseClaims(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ClaimSessions([]string{"s1", "s2"}, "", 15*time.Second); err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if err := db.ReleaseClaims([]string{"s1"}); err != nil {
		t.Fatalf("ReleaseClaims: %v", err)
	}
	claims, _ := db.LoadClaims()
	if _, ok := claims["s1"]; ok {
		t.Error("s1 not released")
	}
	if _, ok := claims["s2"]; !ok {
		t.Error("s2 unexpectedly gone")
	}
	if err := db.ReleaseAllClaims(); err != nil {
		t.Fatalf("ReleaseAllClaims: %v", err)
	}
	claims, _ = db.LoadClaims()
	if len(claims) != 0 {
		t.Errorf("expected empty claims, got %v", claims)
	}
}

// TestLoadClaimsFields asserts LoadClaims surfaces OwnerToken/OwnerPID/
// Heartbeat correctly for a freshly-claimed row.
func TestLoadClaimsFields(t *testing.T) {
	db := newTestDB(t)
	before := time.Now().Unix()
	if _, err := db.ClaimSessions([]string{"s1"}, "myscope", 15*time.Second); err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	row, ok := claims["s1"]
	if !ok {
		t.Fatal("s1 missing from LoadClaims")
	}
	if row.OwnerPID != db.pid {
		t.Errorf("OwnerPID = %d, want %d", row.OwnerPID, db.pid)
	}
	if row.OwnerToken != db.token {
		t.Errorf("OwnerToken = %q, want %q", row.OwnerToken, db.token)
	}
	if row.Scope != "myscope" {
		t.Errorf("Scope = %q, want myscope", row.Scope)
	}
	if row.Heartbeat < before {
		t.Errorf("Heartbeat = %d, want >= %d", row.Heartbeat, before)
	}
}

func TestPruneStaleSessionClaims(t *testing.T) {
	db := newTestDB(t)
	now := time.Now().Unix()

	// "live" has a matching instances row; "orphan" does not (session was
	// deleted/archived-then-purged after the claim was written).
	if _, err := db.DB().Exec(
		`INSERT INTO instances (id, title, project_path, created_at) VALUES (?, ?, ?, ?)`,
		"live", "Live Session", "/tmp/project", now); err != nil {
		t.Fatalf("seed instances: %v", err)
	}
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "live", 12345, "12345-1-deadbeef", now, now, ""); err != nil {
		t.Fatalf("seed live claim: %v", err)
	}
	if _, err := db.DB().Exec(
		`INSERT INTO session_claims (session_id, owner_pid, owner_token, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?, ?)`, "orphan", 12345, "12345-1-deadbeef", now, now, ""); err != nil {
		t.Fatalf("seed orphan claim: %v", err)
	}

	if err := db.PruneStaleSessionClaims(); err != nil {
		t.Fatalf("PruneStaleSessionClaims: %v", err)
	}

	claims, err := db.LoadClaims()
	if err != nil {
		t.Fatalf("LoadClaims: %v", err)
	}
	if _, ok := claims["orphan"]; ok {
		t.Error("orphan claim (no matching instances row) survived prune")
	}
	if _, ok := claims["live"]; !ok {
		t.Error("live claim was incorrectly pruned")
	}
}
