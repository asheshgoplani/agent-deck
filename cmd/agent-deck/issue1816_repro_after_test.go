package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/fleet"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// REPRODUCTION for #1816, the AFTER half.
//
// Same scenario as issue1816_repro_before_test.go — four sessions, one dead
// credential — through the opt-in surface. This file does not compile before the
// fix, because the surface it exercises does not exist there.

// THE FIX: one dead credential is ONE escalation, and it names the credential.
func TestIssue1816_After_OneDeadCredentialProducesOneEscalation(t *testing.T) {
	instances, credDir := setupIssue1816Fleet(t, 4)

	sum := authCredentialSummary(instances, "")
	escalations := sum.Escalations()

	t.Logf("AFTER (--group-by-credential) — %d sessions on ONE credential produce %d escalation:",
		len(instances), len(escalations))
	for _, e := range escalations {
		t.Logf("  %s", e)
	}
	t.Logf("rendered report:\n%s", sum.Format())

	if len(escalations) != 1 {
		t.Fatalf("got %d escalations, want exactly 1:\n%s", len(escalations), strings.Join(escalations, "\n"))
	}
	if sum.Credentials != 1 || sum.Held != 4 {
		t.Fatalf("summary = %+v, want 1 credential holding 4 sessions", sum)
	}
	// It must be actionable: the operator has to know WHICH credential to fix.
	if !strings.Contains(escalations[0], issue1816Account) {
		t.Errorf("escalation does not name the account: %s", escalations[0])
	}
	if !strings.Contains(escalations[0], credDir) {
		t.Errorf("escalation does not name the credential dir: %s", escalations[0])
	}
	if !strings.Contains(escalations[0], "4 session(s) held") {
		t.Errorf("escalation does not account for all four sessions: %s", escalations[0])
	}
}

// THE DEFAULT IS UNTOUCHED: with the flag off, the per-session view is exactly
// what the BEFORE capture recorded — same count, same bytes — and the default
// --json payload has not grown the aggregation key.
func TestIssue1816_After_PerSessionViewUnchangedWithFlagOff(t *testing.T) {
	instances, credDir := setupIssue1816Fleet(t, 4)

	escalations := perSessionEscalations(instances)

	t.Logf("AFTER (flag off) — the per-session view is unchanged: %d escalations", len(escalations))
	for i, e := range escalations {
		t.Logf("  [%d/%d] %s", i+1, len(escalations), e)
	}

	if len(escalations) != 4 {
		t.Fatalf("got %d per-session escalations, want the original 4", len(escalations))
	}
	for _, inst := range instances {
		held, remedy := inst.IsAuthHeld()
		if !held {
			t.Fatalf("%s stopped being auth-held", inst.Title)
		}
		// Byte-for-byte the pre-#1816 remedy: the aggregation must not have
		// leaked into the per-session surface.
		if remedy != (&session.AuthHoldRecord{Reason: session.AuthHoldReasonDeath}).Remedy() {
			t.Errorf("per-session remedy changed:\n%s", remedy)
		}
		if strings.Contains(remedy, credDir) || strings.Contains(remedy, issue1816Account) {
			t.Errorf("per-session remedy grew credential detail with the flag off: %s", remedy)
		}
	}

	// The default machine surface is unchanged too.
	payload := fleetStatusJSON(fleet.Assessment{Total: 4, Down: 4})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "auth_credentials") {
		t.Errorf("default fleet status payload gained the aggregation key: %s", raw)
	}
}

// The opt-in payload is what a conductor consumes instead of N substates.
func TestIssue1816_After_JSONExposesTheCredentialGrouping(t *testing.T) {
	instances, credDir := setupIssue1816Fleet(t, 4)

	payload := fleetStatusJSON(fleet.Assessment{Total: 4, Down: 4})
	payload["auth_credentials"] = fleetAuthCredentialsJSON(authCredentialSummary(instances, ""))

	raw, err := json.MarshalIndent(payload["auth_credentials"], "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	t.Logf("AFTER --json auth_credentials:\n%s", raw)

	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["credentials"].(float64) != 1 {
		t.Errorf("credentials = %v, want 1", got["credentials"])
	}
	if got["held"].(float64) != 4 {
		t.Errorf("held = %v, want 4", got["held"])
	}
	groups := got["groups"].([]interface{})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].(map[string]interface{})["config_dir"] != credDir {
		t.Errorf("config_dir = %v, want %s", groups[0].(map[string]interface{})["config_dir"], credDir)
	}

	// Local-only and identifier-only: nothing token-shaped may appear.
	for _, forbidden := range []string{"sk-ant", "Bearer", "refresh_token", "access_token"} {
		if strings.Contains(string(raw), forbidden) {
			t.Errorf("payload contains %q: %s", forbidden, raw)
		}
	}
}
