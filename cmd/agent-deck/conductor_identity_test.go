package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// `conductor setup` stamps the stable conductor name on the session it
// creates, and on a re-run finds the conductor even after its title was
// decorated or renamed (instead of registering a duplicate), re-stamping it.
func TestRegisterConductorSession_StoresStableName(t *testing.T) {
	sandboxConductorHome(t)
	spec, err := session.GetConductorAgentSpec(session.ConductorAgentClaude)
	if err != nil {
		t.Fatal(err)
	}

	instances, inst, existed := registerConductorSession(nil, "personal", spec)
	if existed || len(instances) != 1 {
		t.Fatalf("fresh setup: existed=%v len=%d", existed, len(instances))
	}
	if inst.ConductorName != "personal" || !inst.IsConductor {
		t.Fatalf("fresh setup did not stamp identity: name=%q is_conductor=%v", inst.ConductorName, inst.IsConductor)
	}

	// Renamed after setup: the re-run must reuse it, not add a second one.
	inst.Title = "my assistant"
	instances, again, existed := registerConductorSession(instances, "personal", spec)
	if !existed || again != inst || len(instances) != 1 {
		t.Fatalf("re-run after rename: existed=%v same=%v len=%d", existed, again == inst, len(instances))
	}

	// Legacy record (no stored name) with a decorated title: reused and
	// stamped so later renames keep resolving.
	// Setup has flagged its rows IsConductor since #476; only a flagged row
	// is matched by a decorated title.
	legacy := session.NewInstanceWithGroupAndTool("🎙 conductor-ops", "/tmp", "conductor", spec.Agent)
	legacy.IsConductor = true
	got := []*session.Instance{legacy}
	got, reused, existed := registerConductorSession(got, "ops", spec)
	if !existed || reused != legacy || len(got) != 1 {
		t.Fatalf("legacy decorated re-run: existed=%v same=%v len=%d", existed, reused == legacy, len(got))
	}
	if legacy.ConductorName != "ops" || !legacy.IsConductor {
		t.Fatalf("legacy re-run did not stamp identity: name=%q is_conductor=%v", legacy.ConductorName, legacy.IsConductor)
	}
}

// ResolveSession is how every out-of-process caller (the generated heartbeat
// script's `session show/send "conductor-<name>"`, conductor bridge.py, user
// scripts) addresses a conductor. An exact title still wins; with no exact
// title or ID match, "conductor-<name>" falls back to the conductor's
// identity so a decorated or renamed conductor stays addressable.
func TestResolveSession_ConductorTitleFallsBackToIdentity(t *testing.T) {
	decorated := &session.Instance{ID: "aaaaaaaaaaaa1111", Title: "🎙 conductor-personal", IsConductor: true}
	renamed := &session.Instance{ID: "bbbbbbbbbbbb2222", Title: "my assistant", IsConductor: true, ConductorName: "ops"}
	worker := &session.Instance{ID: "cccccccccccc3333", Title: "worker"}
	instances := []*session.Instance{decorated, renamed, worker}

	for ident, want := range map[string]*session.Instance{
		"conductor-personal": decorated,
		"conductor-ops":      renamed,
	} {
		got, errMsg, _ := ResolveSession(ident, instances)
		if got != want {
			t.Errorf("ResolveSession(%q) = %v (%s), want %s", ident, got, errMsg, want.ID)
		}
	}

	// Exact title wins over the identity fallback.
	exact := &session.Instance{ID: "dddddddddddd4444", Title: "conductor-ops"}
	if got, _, _ := ResolveSession("conductor-ops", append(instances, exact)); got != exact {
		t.Errorf("exact title match should win, got %v", got)
	}

	// Unknown conductors and bare prefixes stay not-found.
	for _, ident := range []string{"conductor-ghost", "conductor-"} {
		if got, _, code := ResolveSession(ident, instances); got != nil || code != ErrCodeNotFound {
			t.Errorf("ResolveSession(%q) = %v code=%s, want not found", ident, got, code)
		}
	}
}

// saveConductorFixture writes a conductor meta.json for name in the default
// profile and saves instances to that profile's state.db.
func saveConductorFixture(t *testing.T, name string, instances ...*session.Instance) *session.Storage {
	t.Helper()
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: name, Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	storage, err := session.NewStorageWithProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveWithGroups(instances, session.NewGroupTreeWithGroups(instances, nil)); err != nil {
		t.Fatal(err)
	}
	return storage
}

// `conductor teardown --remove` stops the conductor it finds by identity and
// must remove that same row, not only a row whose title is exactly
// conductor-<name>, or a renamed/decorated conductor leaves an orphan row.
func TestConductorTeardownRemove_RenamedAndDecorated(t *testing.T) {
	for _, tc := range []struct {
		name string
		inst func() *session.Instance
	}{
		{"renamed", func() *session.Instance {
			i := session.NewInstanceWithGroupAndTool("my assistant", "/tmp", "conductor", "claude")
			i.IsConductor = true
			i.ConductorName = "ops"
			return i
		}},
		{"decorated", func() *session.Instance {
			i := session.NewInstanceWithGroupAndTool("🎙 conductor-ops", "/tmp", "conductor", "claude")
			i.IsConductor = true
			return i
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sandboxConductorHome(t)
			home := os.Getenv("HOME")
			cond := tc.inst()
			bystander := session.NewInstanceWithGroupAndTool("worker", "/tmp", "work", "claude")
			storage := saveConductorFixture(t, "ops", cond, bystander)
			storage.Close()

			if out, stderr, code := runAgentDeck(t, home, "conductor", "teardown", "ops", "--remove", "--json"); code != 0 {
				t.Fatalf("teardown exited %d: %s %s", code, out, stderr)
			}
			storage, err := session.NewStorageWithProfile("default")
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			loaded, _, err := storage.LoadWithGroups()
			if err != nil {
				t.Fatal(err)
			}
			keptBystander := false
			for _, l := range loaded {
				if l.ID == cond.ID {
					t.Errorf("teardown --remove left the %s conductor row %q in state.db", tc.name, l.Title)
				}
				keptBystander = keptBystander || l.ID == bystander.ID
			}
			if !keptBystander {
				t.Error("teardown --remove deleted an unrelated session row")
			}
		})
	}
}

// `conductor status` surfaces a renamed conductor's title drift in both the
// human output (warning line) and JSON (session_title + title_mismatch).
func TestConductorStatus_ReportsTitleDrift(t *testing.T) {
	sandboxConductorHome(t)
	home := os.Getenv("HOME")
	cond := session.NewInstanceWithGroupAndTool("my assistant", "/tmp", "conductor", "claude")
	cond.IsConductor = true
	cond.ConductorName = "ops"
	saveConductorFixture(t, "ops", cond).Close()

	out, stderr, code := runAgentDeck(t, home, "conductor", "status", "ops", "--json")
	if code != 0 {
		t.Fatalf("status --json exited %d: %s %s", code, out, stderr)
	}
	var status struct {
		Conductors []struct {
			Name          string `json:"name"`
			SessionID     string `json:"session_id"`
			SessionTitle  string `json:"session_title"`
			TitleMismatch bool   `json:"title_mismatch"`
		} `json:"conductors"`
	}
	if err := json.Unmarshal([]byte(out), &status); err != nil {
		t.Fatalf("decode status: %v: %s", err, out)
	}
	if len(status.Conductors) != 1 {
		t.Fatalf("want one conductor: %s", out)
	}
	c := status.Conductors[0]
	if c.SessionID != cond.ID || c.SessionTitle != "my assistant" || !c.TitleMismatch {
		t.Fatalf("status JSON drift = %+v, want session %s titled %q with title_mismatch", c, cond.ID, "my assistant")
	}

	out, stderr, code = runAgentDeck(t, home, "conductor", "status", "ops")
	if code != 0 {
		t.Fatalf("status exited %d: %s %s", code, out, stderr)
	}
	want := `warning: session title "my assistant" no longer matches conductor "ops"`
	if !strings.Contains(out, want) {
		t.Fatalf("status text missing drift warning %q:\n%s", want, out)
	}
}

// The generated heartbeat script runs `session show "conductor-<name>"` and
// `session send "conductor-<name>"`; through the real binary that canonical
// title must still reach a renamed conductor.
func TestSessionShow_CanonicalTitleReachesRenamedConductor(t *testing.T) {
	sandboxConductorHome(t)
	home := os.Getenv("HOME")
	cond := session.NewInstanceWithGroupAndTool("my assistant", "/tmp", "conductor", "claude")
	cond.IsConductor = true
	cond.ConductorName = "ops"
	saveConductorFixture(t, "ops", cond).Close()

	out, stderr, code := runAgentDeckEnv(t, home, "", []string{"AGENTDECK_PROFILE=default"}, "session", "show", "conductor-ops", "--json")
	if code != 0 {
		t.Fatalf("session show conductor-ops exited %d: %s %s", code, out, stderr)
	}
	if !strings.Contains(out, cond.ID) {
		t.Fatalf("session show conductor-ops did not resolve the renamed conductor %s:\n%s", cond.ID, out)
	}
}
