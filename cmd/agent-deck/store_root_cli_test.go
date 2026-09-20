package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// seedStoreCLI creates profiles/<profile>/state.db under root with n rows,
// directly through statedb so both data roots can be shaped independently of
// the resolver under test.
func seedStoreCLI(t *testing.T, root, profile string, n int) string {
	t.Helper()
	dbPath := filepath.Join(root, session.ProfilesDirName, profile, "state.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for i := 0; i < n; i++ {
		row := &statedb.InstanceRow{
			ID:           profile + "-seed-" + string(rune('a'+i)),
			Title:        "legacy-" + string(rune('a'+i)),
			ProjectPath:  root,
			Tool:         "shell",
			Status:       "idle",
			CreatedAt:    time.Now(),
			LastAccessed: time.Now(),
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("save: %v", err)
		}
	}
	return dbPath
}

// strayXDGFixture is the 2026-09-19/20 incident on disk: a populated legacy
// store and an EMPTY XDG store for the same profile. The CLI helper points
// XDG_DATA_HOME at $HOME/.local/share and selects profile ch_support_test.
func strayXDGFixture(t *testing.T) (home, legacyDB, strayDB string) {
	t.Helper()
	home = t.TempDir()
	legacyRoot := filepath.Join(home, ".agent-deck")
	xdgRoot := filepath.Join(home, ".local", "share", "agent-deck")
	legacyDB = seedStoreCLI(t, legacyRoot, "ch_support_test", 3)
	strayDB = seedStoreCLI(t, xdgRoot, "ch_support_test", 0)
	return home, legacyDB, strayDB
}

func fileSignature(t *testing.T, path string) string {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return st.ModTime().String() + "/" + strconv.FormatInt(st.Size(), 10)
}

// TestCLI_StrayEmptyXDGStore_NewProcessesKeepLegacy is the incident from the
// CLI's point of view: with an empty XDG store beside the populated legacy
// one, a fresh `list` process must still show every session, and neither
// `list` nor the hook handler may touch the stray or create anything under
// the XDG profiles dir.
func TestCLI_StrayEmptyXDGStore_NewProcessesKeepLegacy(t *testing.T) {
	home, _, strayDB := strayXDGFixture(t)
	before := fileSignature(t, strayDB)

	stdout, stderr, code := runAgentDeck(t, home, "list", "--json")
	if code != 0 {
		t.Fatalf("list exit %d: %s %s", code, stdout, stderr)
	}
	if got := strings.Count(stdout, `"legacy-`); got != 3 {
		t.Fatalf("list saw %d of the 3 legacy sessions (the stray XDG store was preferred?):\n%s", got, stdout)
	}

	// The hook handler is the status writer that went blind during the
	// incident; it runs as a fresh process per Claude Code hook event.
	payload := `{"hook_event_name":"UserPromptSubmit","session_id":"s1","cwd":"` + home + `"}`
	_, stderr, code = runAgentDeckEnv(t, home, payload, []string{"AGENTDECK_INSTANCE_ID=ch_support_test-seed-a"}, "hook-handler")
	if code != 0 {
		t.Fatalf("hook-handler exit %d: %s", code, stderr)
	}

	if after := fileSignature(t, strayDB); after != before {
		t.Fatalf("stray XDG store was modified by a CLI process: %s -> %s", before, after)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".local", "share", "agent-deck", session.ProfilesDirName))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("XDG profiles dir gained entries: %v", entries)
	}
}

// TestCLI_Doctor_ReportsStoreDivergence checks the operator surface: doctor
// names both roots, their session counts, the active one and a WARNING, in
// text and JSON, without changing HOME.
func TestCLI_Doctor_ReportsStoreDivergence(t *testing.T) {
	home, legacyDB, strayDB := strayXDGFixture(t)
	legacyRoot := filepath.Dir(filepath.Dir(filepath.Dir(legacyDB)))
	xdgRoot := filepath.Dir(filepath.Dir(filepath.Dir(strayDB)))
	before := snapshotTree(t, home)

	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	for _, want := range []string{
		"Profile store: " + legacyRoot + " (legacy, reason=stray_xdg_store)",
		legacyRoot + ": 3 sessions (ch_support_test=3) [active]",
		xdgRoot + ": 0 sessions (ch_support_test=0)",
		"WARNING: stray empty XDG profile store at " + filepath.Join(xdgRoot, session.ProfilesDirName),
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, stdout)
		}
	}

	stdout, stderr, code = runAgentDeck(t, home, "doctor", "--json")
	if code != 0 {
		t.Fatalf("doctor --json exit %d: %s %s", code, stdout, stderr)
	}
	var report struct {
		StoreRoots session.StoreRootSelection `json:"store_roots"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("doctor JSON: %v: %s", err, stdout)
	}
	sel := report.StoreRoots
	if sel.Active != legacyRoot || sel.Kind != "legacy" || sel.Reason != session.StoreRootReasonStrayXDG || !sel.Divergent {
		t.Fatalf("store_roots = %+v", sel)
	}
	if sel.Legacy.Sessions != 3 || sel.XDG.Sessions != 0 || sel.XDG.Profiles["ch_support_test"] != 0 {
		t.Fatalf("store_roots counts = legacy %+v xdg %+v", sel.Legacy, sel.XDG)
	}

	stdout, stderr, code = runAgentDeck(t, home, "health")
	if code != 0 {
		t.Fatalf("health exit %d: %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "profile store divergence: stray empty XDG profile store") {
		t.Fatalf("health output lacks the divergence flag:\n%s", stdout)
	}

	// Read-only diagnostics create nothing under HOME (SQLite WAL sidecars
	// of the read-only live open excepted).
	for path := range snapshotTree(t, home) {
		if before[path] || strings.HasSuffix(path, "-shm") || strings.HasSuffix(path, "-wal") {
			continue
		}
		t.Errorf("doctor/health created %s", path)
	}
}

// TestCLI_Doctor_CleanLayoutHasNoStoreWarning: a single-root layout prints
// the active store and no WARNING line.
func TestCLI_Doctor_CleanLayoutHasNoStoreWarning(t *testing.T) {
	home := t.TempDir()
	legacyRoot := filepath.Join(home, ".agent-deck")
	seedStoreCLI(t, legacyRoot, "ch_support_test", 2)

	stdout, stderr, code := runAgentDeck(t, home, "doctor")
	if code != 0 {
		t.Fatalf("doctor exit %d: %s %s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Profile store: "+legacyRoot+" (legacy, reason=legacy_only)") {
		t.Fatalf("doctor output lacks the active store line:\n%s", stdout)
	}
	if strings.Contains(stdout, "stray") || strings.Contains(stdout, "profile stores exist under both") {
		t.Fatalf("clean layout must not warn:\n%s", stdout)
	}
}

// TestStoreSelection_LogsOnce checks the structured lines: one
// `store_selected` per process for the decision and a WARN
// `stray_xdg_store` naming the stray path.
func TestStoreSelection_LogsOnce(t *testing.T) {
	logDir := initTestLogging(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	session.ResetStoreRootSelection()
	t.Cleanup(session.ResetStoreRootSelection)
	legacyRoot := filepath.Join(home, ".agent-deck")
	xdgRoot := filepath.Join(home, ".local", "share", "agent-deck")
	seedStoreCLI(t, legacyRoot, "personal", 2)
	seedStoreCLI(t, xdgRoot, "personal", 0)

	for i := 0; i < 3; i++ {
		if _, err := session.SelectStoreRoot(); err != nil {
			t.Fatal(err)
		}
	}
	body := readLogTolerant(t, logDir)
	if got := strings.Count(body, `"store_selected"`); got != 1 {
		t.Fatalf("store_selected logged %d times, want once; log:\n%s", got, body)
	}
	if !strings.Contains(body, `"path":"`+legacyRoot+`"`) || !strings.Contains(body, `"reason":"stray_xdg_store"`) {
		t.Fatalf("store_selected lacks path/reason; log:\n%s", body)
	}
	if got := strings.Count(body, `"stray_xdg_store"`); got < 1 {
		t.Fatalf("stray_xdg_store WARN missing; log:\n%s", body)
	}
	if !strings.Contains(body, `"level":"WARN"`) || !strings.Contains(body, filepath.Join(xdgRoot, session.ProfilesDirName)) {
		t.Fatalf("stray_xdg_store WARN lacks level/path; log:\n%s", body)
	}
}
