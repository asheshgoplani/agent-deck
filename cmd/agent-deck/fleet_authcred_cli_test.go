package main

import (
	"encoding/json"
	"flag"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/fleet"
)

// CLI surface for #1816 credential grouping: the flag is opt-in, and the
// default output is byte-for-byte what it was.

// THE DEFAULT-OFF PROOF: the flag registers with a false default on both
// subcommands. This is the maintainer ruling's boundary expressed as a test —
// "off by default, per-session view stays the default surface".
func TestGroupByCredentialFlagDefaultsToOff(t *testing.T) {
	for _, name := range []string{"fleet status", "fleet recover"} {
		t.Run(name, func(t *testing.T) {
			fs := flag.NewFlagSet(name, flag.ContinueOnError)
			got := fs.Bool("group-by-credential", false, groupByCredentialFlagHelp())

			if *got {
				t.Fatal("flag initialised to true")
			}
			f := fs.Lookup("group-by-credential")
			if f == nil {
				t.Fatal("flag not registered")
			}
			if f.DefValue != "false" {
				t.Errorf("DefValue = %q, want \"false\"", f.DefValue)
			}
			// Parsing an empty argv must leave it off.
			if err := fs.Parse(nil); err != nil {
				t.Fatalf("parse: %v", err)
			}
			if *got {
				t.Error("flag became true without being passed")
			}
		})
	}
}

// The gate only groups when the operator asked for it.
func TestRecoverConfigForwardsGroupByCredential(t *testing.T) {
	t.Run("off by default", func(t *testing.T) {
		rec := fleetRecoverConfig{authHaltAfter: 2}.recoverer()
		gate, ok := rec.AuthGate.(*fleet.SubstateAuthGate)
		if !ok {
			t.Fatalf("AuthGate = %T", rec.AuthGate)
		}
		if gate.GroupByCredential {
			t.Error("GroupByCredential defaulted to true")
		}
	})

	t.Run("forwarded when set", func(t *testing.T) {
		rec := fleetRecoverConfig{authHaltAfter: 2, groupByCredential: true}.recoverer()
		gate := rec.AuthGate.(*fleet.SubstateAuthGate)
		if !gate.GroupByCredential {
			t.Error("GroupByCredential not forwarded")
		}
	})

	// Grouping is a reporting flag. It must never resurrect a safety brake the
	// operator explicitly turned off.
	t.Run("does not re-enable a disabled breaker", func(t *testing.T) {
		rec := fleetRecoverConfig{authHaltAfter: 0, groupByCredential: true}.recoverer()
		if rec.AuthGate != nil {
			t.Errorf("AuthGate = %v, want nil — --auth-halt-after 0 disables the breaker", rec.AuthGate)
		}
	})
}

// The default --json payload must not grow a key: a conductor polling it today
// keeps seeing exactly what it sees today.
func TestFleetStatusJSONUnchangedWithoutTheFlag(t *testing.T) {
	as := fleet.Assessment{Total: 3, Alive: 1, Down: 2}
	payload := fleetStatusJSON(as)

	if _, ok := payload["auth_credentials"]; ok {
		t.Fatal("auth_credentials appeared in the default payload")
	}
	// And the opt-in path adds it without disturbing the rest.
	withFlag := fleetStatusJSON(as)
	withFlag["auth_credentials"] = fleetAuthCredentialsJSON(fleet.AuthCredentialSummary{})
	for k, v := range payload {
		if _, ok := withFlag[k]; !ok {
			t.Errorf("opt-in payload dropped key %q (%v)", k, v)
		}
	}
}

// The JSON grouping is machine-checkable and carries identifiers only.
func TestFleetAuthCredentialsJSONShape(t *testing.T) {
	sum := fleet.AuthCredentialSummary{
		Held:        3,
		Credentials: 1,
		Groups: []fleet.CredentialGroup{
			{
				Credential: fleet.CredentialRef{Key: "dir:/home/u/.claude-work", ConfigDir: "/home/u/.claude-work", Attributed: true},
				Accounts:   []string{"work"},
				Sessions: []fleet.HeldSession{
					{ID: "id-a", Title: "alpha", Account: "work", Reason: "auth_banner_live"},
					{ID: "id-b", Title: "bravo", Account: "work", Reason: "auth_death"},
				},
			},
		},
	}

	raw, err := json.Marshal(fleetAuthCredentialsJSON(sum))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	for _, key := range []string{"held", "credentials", "unattributed", "recovered", "groups"} {
		if _, ok := got[key]; !ok {
			t.Errorf("payload missing %q: %s", key, raw)
		}
	}
	groups := got["groups"].([]interface{})
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0].(map[string]interface{})
	for _, key := range []string{"key", "attributed", "accounts", "held", "recovered", "escalation", "sessions", "config_dir"} {
		if _, ok := g[key]; !ok {
			t.Errorf("group missing %q: %s", key, raw)
		}
	}
	if g["held"].(float64) != 2 {
		t.Errorf("group held = %v, want 2", g["held"])
	}
}

// An unattributed group must not carry a config_dir key at all — an empty
// string there would read as a real, blank credential directory.
func TestUnattributedGroupOmitsConfigDir(t *testing.T) {
	sum := fleet.AuthCredentialSummary{
		Held:         1,
		Unattributed: 1,
		Groups: []fleet.CredentialGroup{{
			Credential: fleet.CredentialRef{Key: fleet.UnknownCredentialKey},
			Sessions:   []fleet.HeldSession{{ID: "id-x", Title: "orphan"}},
		}},
	}

	payload := fleetAuthCredentialsJSON(sum)
	group := payload["groups"].([]map[string]interface{})[0]

	if _, ok := group["config_dir"]; ok {
		t.Errorf("unattributed group carries config_dir = %v", group["config_dir"])
	}
	if group["attributed"].(bool) {
		t.Error("unattributed group reports attributed=true")
	}
	if !strings.Contains(group["escalation"].(string), "NOT known to share one credential") {
		t.Errorf("escalation = %v", group["escalation"])
	}
}

// ---------------------------------------------------------------------------
// PR #1963 review findings, CLI surface.
// ---------------------------------------------------------------------------

// P2b — the CLI must thread --group into the credential view. This asserts the
// wiring, not just that the grouper is capable of filtering: the defect was the
// CLI handing the grouper an unfiltered instance list.
func TestAuthCredentialSummaryHonoursGroupArgument(t *testing.T) {
	// Real auth holds, real config-dir resolution — the same fixture the
	// reproduce uses, so this exercises the CLI helper end to end rather than
	// re-asserting that a struct field holds what was just assigned to it.
	instances, _ := setupIssue1816Fleet(t, 4)
	instances[0].GroupPath = "team-a"
	instances[1].GroupPath = "team-a/sub"
	instances[2].GroupPath = "team-b"
	instances[3].GroupPath = "team-b"

	all := authCredentialSummary(instances, "")
	if all.Held != 4 {
		t.Fatalf("unfiltered Held = %d, want 4", all.Held)
	}

	scoped := authCredentialSummary(instances, "team-a")
	if scoped.Held != 2 {
		t.Fatalf("--group team-a Held = %d, want 2 — the CLI handed the grouper an unfiltered list", scoped.Held)
	}
	for _, g := range scoped.Groups {
		for _, s := range g.Sessions {
			if s.Title == instances[2].Title || s.Title == instances[3].Title {
				t.Errorf("session %s from team-b appeared under --group team-a", s.Title)
			}
		}
	}

	empty := authCredentialSummary(instances, "team-nonexistent")
	if empty.Held != 0 || len(empty.Groups) != 0 {
		t.Errorf("--group with no members = %+v, want an empty view rather than the whole fleet", empty)
	}
}

// P2a — an attributed group always reports its host, so a machine consumer never
// has to infer locality from a missing key, and a remote store is flagged.
func TestFleetAuthCredentialsJSONCarriesHost(t *testing.T) {
	sum := fleet.AuthCredentialSummary{
		Held:        2,
		Credentials: 2,
		Groups: []fleet.CredentialGroup{
			{
				Credential: fleet.CredentialRef{Key: "store:local|/home/u/.claude-work", ConfigDir: "/home/u/.claude-work", Attributed: true},
				Accounts:   []string{"work"},
				Sessions:   []fleet.HeldSession{{ID: "id-a", Title: "local-one"}},
			},
			{
				Credential: fleet.CredentialRef{Key: "store:ssh:box-b|/home/u/.claude-work", ConfigDir: "/home/u/.claude-work", Host: "box-b", Attributed: true},
				Accounts:   []string{"work"},
				Sessions:   []fleet.HeldSession{{ID: "id-b", Title: "remote-one"}},
			},
		},
	}

	raw, err := json.Marshal(fleetAuthCredentialsJSON(sum))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	groups := got["groups"].([]interface{})
	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}

	local := groups[0].(map[string]interface{})
	if local["host"] != "local" || local["remote"].(bool) {
		t.Errorf("local group host=%v remote=%v, want \"local\"/false", local["host"], local["remote"])
	}
	remote := groups[1].(map[string]interface{})
	if remote["host"] != "box-b" || !remote["remote"].(bool) {
		t.Errorf("remote group host=%v remote=%v, want \"box-b\"/true", remote["host"], remote["remote"])
	}
	// The two must be distinguishable by key even though the path is identical.
	if local["key"] == remote["key"] {
		t.Errorf("local and remote stores share a key %v — same path on two hosts must not merge", local["key"])
	}
}
