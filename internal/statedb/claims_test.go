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
		`INSERT INTO session_claims (session_id, owner_pid, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?)`, "s1", 99999, now, now, "fitstars"); err != nil {
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
		`INSERT INTO session_claims (session_id, owner_pid, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?)`, "s1", 99999, stale, stale, "fitstars"); err != nil {
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
		`INSERT INTO session_claims (session_id, owner_pid, claimed_at, heartbeat, scope)
		 VALUES (?, ?, ?, ?, ?)`, "s1", 99999, now, now, "fitstars"); err != nil {
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

func TestRefreshAndReleaseClaims(t *testing.T) {
	db := newTestDB(t)
	if _, err := db.ClaimSessions([]string{"s1", "s2"}, "", 15*time.Second); err != nil {
		t.Fatalf("ClaimSessions: %v", err)
	}
	if err := db.RefreshClaimHeartbeats(); err != nil {
		t.Fatalf("RefreshClaimHeartbeats: %v", err)
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
