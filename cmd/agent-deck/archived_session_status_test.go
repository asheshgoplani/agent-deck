package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// Archiving tears down a session's pane, and a session whose pane is already
// dead (after a reboot, say) reads as error. Archiving one of those used to
// keep the error, so every heartbeat reported each archived session as a
// crash. Archive must record stopped, show --json must say archived, and
// neither status nor the heartbeat may count archived sessions at all.
func TestArchivedDeadSession_IsStoppedAndLeftOutOfCounts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	project := filepath.Join(home, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := session.SaveConductorMeta(&session.ConductorMeta{Name: "ops", Profile: "default"}); err != nil {
		t.Fatal(err)
	}
	addSession := func(args ...string) string {
		t.Helper()
		out, stderr, code := runAgentDeck(t, home, append([]string{"add", "-c", "claude", "--no-parent", "--json"}, append(args, project)...)...)
		if code != 0 {
			t.Fatalf("add %v: %d %s %s", args, code, out, stderr)
		}
		var added struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(out), &added); err != nil || added.ID == "" {
			t.Fatalf("add response: %v %s", err, out)
		}
		return added.ID
	}
	conductorID := addSession("-t", "conductor-ops")
	addSession("-t", "kept", "-g", "ops")
	crashedID := addSession("-t", "crashed", "-g", "ops")

	// A pending inbox record makes the tick print even with nothing to flag,
	// so the counts it reports are visible.
	inbox := session.InboxPathFor(conductorID)
	if err := os.MkdirAll(filepath.Dir(inbox), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(inbox, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	setStoredStatus(t, home, crashedID, session.StatusError)

	if out, stderr, code := runAgentDeck(t, home, "session", "archive", "crashed", "--json"); code != 0 {
		t.Fatalf("archive: %d %s %s", code, out, stderr)
	}
	if got := storedStatus(t, home, crashedID); got != string(session.StatusStopped) {
		t.Fatalf("stored status after archiving a dead session = %q, want stopped", got)
	}

	out, stderr, code := runAgentDeck(t, home, "session", "show", "crashed", "--json")
	if code != 0 {
		t.Fatalf("show: %d %s %s", code, out, stderr)
	}
	var shown map[string]any
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("decode show: %v: %s", err, out)
	}
	if shown["archived"] != true || shown["archived_at"] == nil {
		t.Fatalf("show --json must mark the session archived: %s", out)
	}

	out, stderr, code = runAgentDeck(t, home, "status", "--json")
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out, stderr)
	}
	var counts struct {
		Error, Stopped, Total int
	}
	if err := json.Unmarshal([]byte(out), &counts); err != nil {
		t.Fatalf("decode status: %v: %s", err, out)
	}
	if counts.Error != 0 || counts.Stopped != 0 || counts.Total != 2 {
		t.Fatalf("status must count only the conductor and the kept session: %s", out)
	}

	out, stderr, code = runAgentDeck(t, home, "conductor", "heartbeat-tick", "ops")
	if code != 0 {
		t.Fatalf("heartbeat-tick: %d %s %s", code, out, stderr)
	}
	if !strings.Contains(out, "1 idle, 0 error, 0 stopped.") || strings.Contains(out, "crashed") {
		t.Fatalf("heartbeat must leave the archived session out: %q", out)
	}
}

// openSubprocessDB opens the state.db that runAgentDeck's binary writes under
// home. The binary runs with the helper's own profile and environment, so the
// test finds the one database there rather than resolving a path in-process.
func openSubprocessDB(t *testing.T, home string) *statedb.StateDB {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(home, ".local", "share", "agent-deck", "profiles", "*", "state.db"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("want exactly one state.db under %s, got %v (%v)", home, paths, err)
	}
	db, err := statedb.Open(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func setStoredStatus(t *testing.T, home, id string, status session.Status) {
	t.Helper()
	db := openSubprocessDB(t, home)
	if err := db.PersistInstanceStatusesTx([]statedb.InstanceStatusUpdate{{ID: id, Status: string(status)}}); err != nil {
		t.Fatal(err)
	}
}

func storedStatus(t *testing.T, home, id string) string {
	t.Helper()
	row, err := openSubprocessDB(t, home).LoadInstanceByID(id)
	if err != nil || row == nil {
		t.Fatalf("load session %s: %v", id, err)
	}
	return row.Status
}
